// Package fetch sends HTTP requests for the agent with a secret it cannot
// read, such as a GitHub token.
//
// den adds the secret itself and sends it only to the service's own hosts,
// so the agent can use the service but cannot copy the secret elsewhere.
// Reads go out at once. A write that reaches other people or cannot be
// undone waits for the user to allow it in Telegram.
package fetch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/unsady/den/internal/approve"
	"github.com/unsady/den/internal/config"
	googleauth "github.com/unsady/den/internal/google"
)

// MaxBody bounds a request's and a response's body.
const MaxBody = 20 << 20

// Request is what the agent asks den to send.
type Request struct {
	Secret string            `json:"secret"`
	Method string            `json:"method"`
	URL    string            `json:"url"`
	Header map[string]string `json:"header,omitempty"`
	Body   []byte            `json:"body,omitempty"`
	// Turn is the DEN_TURN of the chat's agent that sent the request, which
	// den tells what came of a request that waited for the user.
	Turn string `json:"turn,omitempty"`
}

// Response is what the service answered.
type Response struct {
	Status int               `json:"status"`
	Header map[string]string `json:"header,omitempty"`
	Body   []byte            `json:"body,omitempty"`
	// Waiting says the request waits for the user, and den sends it once
	// they allow it.
	Waiting bool `json:"waiting,omitempty"`
}

// Errors that are the agent's or the user's, not the service's.
var (
	ErrRequest = errors.New("bad request")
	ErrSecret  = errors.New("secret is not set")
)

// service is how den uses one secret.
type service struct {
	label string
	// file is the secret's file in the secrets directory.
	file string
	// token gives the secret instead of a file, such as an access token den
	// refreshes.
	token func(ctx context.Context, f *Fetcher) (string, error)
	// hosts receive the secret. A redirect may go to follow as well, and
	// Go drops the secret on a redirect to another host.
	hosts  []string
	follow []string
	// authorize adds the secret to a request.
	authorize func(r *http.Request, secret string)
	// review says why a request needs the user's approval, or "" when it
	// does not.
	review func(ctx context.Context, f *Fetcher, s service, method string, u *url.URL, body []byte) string
	// preview shows what a request does when its body does not say, such
	// as the draft that drafts/send sends. den takes it again before it
	// sends the request, and sends it only when nothing changed.
	preview func(ctx context.Context, f *Fetcher, s service, u *url.URL, body []byte) (string, error)
	// refuse says why den does not send a request at all, or "".
	refuse func(u *url.URL) string
	// show formats a body for the user to read, when the plain one would not
	// say much.
	show func(body []byte) string
	// always lets the user allow a kind of request for good, where the
	// method and the path say what it does.
	always bool
}

// services are the secrets den can send, by name.
var services = map[string]service{
	"github": github,
	"google": google,
}

// Later asks the user to allow an action without waiting for the answer,
// as approve.Asker.Later does with den's own context.
type Later func(q approve.Question, then func(error)) (bool, error)

type Fetcher struct {
	cfg   config.Config
	later Later
	// Note logs a request that changed something without a question.
	Note func(what, outcome string)
	// Report tells the agent of turn what came of a request that waited for
	// the user: agent in full, user in one line for the user when the turn
	// is gone.
	Report func(turn, agent, user string)
	google *googleauth.Google
	client *http.Client

	mu sync.Mutex
	// logins caches the account each secret belongs to, by secret.
	logins map[string]string
}

func New(cfg config.Config, later Later, google *googleauth.Google) *Fetcher {
	f := &Fetcher{cfg: cfg, later: later, google: google, logins: map[string]string{}}
	f.client = &http.Client{Timeout: 5 * time.Minute, CheckRedirect: f.redirect}
	return f
}

// redirect follows a redirect only to the hosts of the service the request
// went to.
func (f *Fetcher) redirect(r *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("too many redirects")
	}
	s := r.Context().Value(serviceKey{}).(service)
	if r.URL.Scheme != "https" || !slices.Contains(append(s.hosts, s.follow...), r.URL.Hostname()) {
		return http.ErrUseLastResponse
	}
	return nil
}

type serviceKey struct{}

// Do sends req with its secret, after the user allowed it when the
// service's rules ask for that.
func (f *Fetcher) Do(ctx context.Context, req Request) (Response, error) {
	s, ok := f.lookup(req.Secret)
	if !ok {
		return Response{}, fmt.Errorf("%w: den has no secret %q; it knows %s. The user adds an API with den service add on their computer", ErrRequest, req.Secret, strings.Join(f.names(), ", "))
	}
	u, err := url.Parse(req.URL)
	if err != nil || u.Scheme != "https" || !slices.Contains(s.hosts, u.Hostname()) || u.User != nil {
		return Response{}, fmt.Errorf("%w: the %s secret goes only to https://%s", ErrRequest, req.Secret, strings.Join(s.hosts, ", https://"))
	}
	method := strings.ToUpper(req.Method)
	if method == "" {
		method = http.MethodGet
	}
	if s.refuse != nil {
		if why := s.refuse(u); why != "" {
			return Response{}, fmt.Errorf("%w: %s", ErrRequest, why)
		}
	}
	// A header that names another method would make den review one
	// request while the service runs another.
	for k := range req.Header {
		if strings.Contains(strings.ToLower(k), "method") {
			return Response{}, fmt.Errorf("%w: den does not pass on the %s header; use -X", ErrRequest, k)
		}
	}

	secret, err := f.secret(ctx, s)
	if err != nil {
		return Response{}, err
	}

	what := fmt.Sprintf("%s: %s %s", s.label, method, u.Path)
	if why := s.review(ctx, f, s, method, u, req.Body); why != "" {
		text := fmt.Sprintf("%s: %s %s\n%s", s.label, method, u.RequestURI(), why)
		display := show
		if s.show != nil {
			display = s.show
		}
		seen := ""
		if s.preview != nil {
			if seen, err = s.preview(ctx, f, s, u, req.Body); err != nil {
				return Response{}, fmt.Errorf("%w: %v", ErrRequest, err)
			}
		}
		if seen != "" {
			text += "\n\n" + seen
		} else if body := display(req.Body); body != "" {
			text += "\n\n" + body
		}
		q := approve.Question{Text: text, What: what}
		// Always fits a request whose method and path say what it does: not
		// a query string, nor GraphQL, where the body decides.
		if s.always && u.RawQuery == "" && strings.Trim(u.Path, "/") != "graphql" {
			q.Rule = "fetch " + req.Secret + " " + method + " " + pathPattern(u.Path)
			q.Label = s.label + ": " + method + " " + pathPattern(u.Path)
		}
		// The request does not wait for the answer: den sends it once the
		// user allows it, and tells the agent what came of it.
		waiting, err := f.later(q, func(err error) { f.sendLater(s, req, method, u, what, seen, err) })
		if err != nil {
			log.Printf("fetch: %s %s %s: not sent: %v", req.Secret, method, u, err)
			return Response{}, fmt.Errorf("not sent: %w", err)
		}
		if waiting {
			return Response{Waiting: true}, nil
		}
	} else if method != http.MethodGet && method != http.MethodHead {
		log.Printf("fetch: %s %s %s", req.Secret, method, u)
		if f.Note != nil {
			f.Note(what, approve.Went)
		}
	}
	return f.send(ctx, s, secret, method, u, req)
}

// sendLater sends a request the user answered, and reports what came of it.
func (f *Fetcher) sendLater(s service, req Request, method string, u *url.URL, what, seen string, answer error) {
	if f.Report == nil {
		return
	}
	if answer != nil {
		log.Printf("fetch: %s %s %s: not sent: %v", req.Secret, method, u, answer)
		if !errors.Is(answer, context.Canceled) {
			f.Report(req.Turn, fmt.Sprintf("[den: %s was not sent: %v.]", what, answer), "")
		}
		return
	}
	log.Printf("fetch: %s %s %s: allowed by the user", req.Secret, method, u)

	// An access token may have run out while the request waited.
	ctx := context.Background()
	resp, err := func() (Response, error) {
		secret, err := f.secret(ctx, s)
		if err != nil {
			return Response{}, err
		}
		if seen != "" {
			now, err := s.preview(ctx, f, s, u, req.Body)
			if err != nil {
				return Response{}, err
			}
			if now != seen {
				return Response{}, errors.New("what it sends changed after the user saw it, so den did not send it")
			}
		}
		return f.send(ctx, s, secret, method, u, req)
	}()
	if err != nil {
		f.Report(req.Turn, fmt.Sprintf("[den: the user allowed %s, and it failed: %v.]", what, err), fmt.Sprintf("%s failed: %v", what, err))
		return
	}
	body := string(resp.Body)
	if len(body) > 4000 {
		body = body[:4000] + "…"
	}
	f.Report(req.Turn, fmt.Sprintf("[den: the user allowed %s, and den sent it. The service answered %d:\n%s]", what, resp.Status, body), fmt.Sprintf("%s: %d", what, resp.Status))
}

// send sends req with its secret.
func (f *Fetcher) send(ctx context.Context, s service, secret, method string, u *url.URL, req Request) (Response, error) {
	ctx = context.WithValue(ctx, serviceKey{}, s)
	r, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(req.Body))
	if err != nil {
		return Response{}, fmt.Errorf("%w: %v", ErrRequest, err)
	}
	for k, v := range req.Header {
		switch http.CanonicalHeaderKey(k) {
		case "Authorization", "Cookie", "Host", "Proxy-Authorization":
			continue
		}
		r.Header.Set(k, v)
	}
	s.authorize(r, secret)

	resp, err := f.client.Do(r)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
	if err != nil {
		return Response{}, err
	}
	if len(body) > MaxBody {
		return Response{}, fmt.Errorf("the response is over %d MB", MaxBody>>20)
	}

	out := Response{Status: resp.StatusCode, Body: body, Header: map[string]string{}}
	for _, k := range []string{"Content-Type", "Link", "Location", "Retry-After", "X-Ratelimit-Remaining", "X-Ratelimit-Reset"} {
		if v := resp.Header.Get(k); v != "" {
			out.Header[k] = v
		}
	}
	return out, nil
}

func (f *Fetcher) secret(ctx context.Context, s service) (string, error) {
	if s.token != nil {
		secret, err := s.token(ctx, f)
		if errors.Is(err, googleauth.ErrNotConnected) || errors.Is(err, googleauth.ErrClient) {
			return "", fmt.Errorf("%w: %v", ErrSecret, err)
		}
		return secret, err
	}
	data, err := os.ReadFile(filepath.Join(f.cfg.Secrets, s.file))
	secret := strings.TrimSpace(string(data))
	if err != nil || secret == "" {
		return "", fmt.Errorf("%w: the user can run den secret set %s on their computer, and den will use it", ErrSecret, s.file)
	}
	return secret, nil
}

func (f *Fetcher) names() []string {
	var list []string
	for name := range services {
		list = append(list, name)
	}
	custom, _ := LoadCustom(f.cfg)
	for _, c := range custom {
		list = append(list, c.Name)
	}
	slices.Sort(list)
	return list
}

// show formats a request body for the user to read: indented when it is
// JSON, cut when it is long.
func show(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var v any
	text := string(body)
	if json.Unmarshal(body, &v) == nil {
		if pretty, err := json.MarshalIndent(v, "", "  "); err == nil {
			text = string(pretty)
		}
	}
	if runes := []rune(text); len(runes) > 1500 {
		text = fmt.Sprintf("%s…\n(%d more characters not shown)", string(runes[:1500]), len(runes)-1500)
	}
	return text
}

// uuid matches a UUID, which names one thing like a number does.
var uuid = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// isID says whether a path segment names one thing, such as an issue
// number or a UUID, rather than a kind of thing. Names, such as a
// repository's, stay as they are, so a rule never covers another one.
func isID(segment string) bool {
	return segment != "" && strings.Trim(segment, "0123456789") == "" || uuid.MatchString(segment)
}

// pathPattern turns ids in a path into *, so a rule for
// /repos/o/r/issues/12/comments covers the comments of every issue of that
// repository. A repository's owner and name stay, even when numeric.
func pathPattern(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		named := i <= 3 && len(parts) > 1 && parts[1] == "repos"
		if !named && isID(p) {
			parts[i] = "*"
		}
	}
	return strings.Join(parts, "/")
}
