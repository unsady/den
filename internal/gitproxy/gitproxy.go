// Package gitproxy lets the agent clone from and push to GitHub with the
// user's token, which it never sees.
//
// den serves git's smart HTTP protocol on the container's loopback address,
// and git's url.insteadOf sends https://github.com/ there. den adds the
// token and passes each request on to github.com. Before a push goes on,
// den reads the branches it changes: a branch the agent created through den
// changes at once. Anything else waits until the user allows it in
// Telegram: den keeps the push and refuses it for now, so git does not wait,
// and sends the same push to GitHub once the user allows it.
package gitproxy

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/unsady/den/internal/approve"
	"github.com/unsady/den/internal/config"
)

const (
	// Addr is where den serves git.
	Addr = "127.0.0.1:4300"
	// TokenFile is the secret with the user's GitHub token.
	TokenFile = "github_token"
	// branchesFile keeps the branches the agent created, in den's state.
	branchesFile = "git-branches.json"
)

// Later asks the user to allow an action without waiting for the answer,
// as approve.Asker.Later does with den's own context.
type Later func(q approve.Question, then func(error)) (bool, error)

// maxHeld bounds a push den keeps while it waits for the user.
const maxHeld = 64 << 20

// paths are the requests of git's smart HTTP protocol for one repository.
var paths = regexp.MustCompile(`^/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?/(info/refs|git-upload-pack|git-receive-pack)$`)

type Proxy struct {
	cfg   config.Config
	later Later
	// Note logs a push that went without a question.
	Note func(what, outcome string)
	// Report tells the agent what came of a push that waited for the user,
	// as the fetcher's Report does.
	Report func(turn, agent, user string)
	// Turn names the chat's turn running now, if any, which most likely
	// pushed: git carries no DEN_TURN.
	Turn func() string
	// upstream is GitHub's address; tests change it.
	upstream  string
	transport http.RoundTripper

	mu sync.Mutex
	// mine are the branches the agent created, as owner/repo:branch.
	mine map[string]bool
}

func New(cfg config.Config, later Later) *Proxy {
	p := &Proxy{cfg: cfg, later: later, upstream: "https://github.com", mine: map[string]bool{}}
	data, err := os.ReadFile(filepath.Join(cfg.State, branchesFile))
	if err == nil {
		var list []string
		json.Unmarshal(data, &list)
		for _, b := range list {
			p.mine[b] = true
		}
	}
	return p
}

// Serve serves git until ctx ends.
func (p *Proxy) Serve(ctx context.Context) error {
	l, err := net.Listen("tcp", Addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: p}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	if err := srv.Serve(l); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m := paths.FindStringSubmatch(r.URL.Path)
	if m == nil {
		http.Error(w, "den passes on only git requests for GitHub repositories", http.StatusNotFound)
		return
	}
	owner, repo, op := m[1], m[2], m[3]

	// A push's branches become the agent's only once GitHub took them, so
	// a push GitHub refuses cannot claim a branch, such as main.
	var pushed []command
	if op == "git-receive-pack" && r.Method == http.MethodPost {
		body, commands, caps, err := readCommands(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(body)
		if q := p.question(owner+"/"+repo, commands); q != nil {
			if !p.hold(w, r, owner, repo, body, commands, caps, *q) {
				return
			}
		} else if p.Note != nil {
			p.Note(pushName(owner+"/"+repo, commands), approve.Went)
		}
		pushed = commands
	}

	target, _ := url.Parse(p.upstream + "/" + owner + "/" + repo + ".git/" + op)
	target.RawQuery = r.URL.RawQuery
	token := p.token()
	proxy := &httputil.ReverseProxy{
		Rewrite: func(out *httputil.ProxyRequest) {
			out.Out.URL = target
			out.Out.Host = target.Host
			out.Out.Header.Del("Authorization")
			out.Out.Header.Del("Cookie")
			// A public repository needs no token.
			if token != "" {
				out.Out.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token)))
			}
		},
		FlushInterval: -1,
		Transport:     p.transport,
	}
	if pushed != nil {
		proxy.ModifyResponse = func(resp *http.Response) error {
			if resp.StatusCode == http.StatusOK {
				resp.Body = &watched{ReadCloser: resp.Body, done: func(out []byte) {
					took, _, err := readReport(out)
					if err != nil {
						log.Printf("git: %s/%s: %v", owner, repo, err)
					}
					p.record(owner+"/"+repo, pushed, took)
				}}
			}
			return nil
		}
	}
	proxy.ServeHTTP(w, r)
}

func (p *Proxy) token() string {
	data, _ := os.ReadFile(filepath.Join(p.cfg.Secrets, TokenFile))
	return strings.TrimSpace(string(data))
}

// command is one ref a push changes.
type command struct {
	old, new, ref string
}

func zero(id string) bool { return strings.Trim(id, "0") == "" }

// question asks the user about a push unless it only creates branches, or
// changes ones the agent created. It returns nil when the push goes at once.
func (p *Proxy) question(repo string, commands []command) *approve.Question {
	var asked, names []string
	// Changes to existing branches can be allowed for good; deleting a
	// branch or pushing a tag is asked every time.
	lasting := true
	for _, c := range commands {
		branch, isBranch := strings.CutPrefix(c.ref, "refs/heads/")
		p.mu.Lock()
		mine := p.mine[repo+":"+branch]
		p.mu.Unlock()
		if isBranch && (zero(c.old) || mine) {
			continue
		}
		asked = append(asked, describe(c))
		names = append(names, branch)
		if !isBranch || zero(c.new) {
			lasting = false
		}
	}
	if len(asked) == 0 {
		return nil
	}

	q := approve.Question{Text: "⇡ Push to " + repo + "\n\n" + strings.Join(asked, "\n"), What: pushName(repo, commands)}
	if lasting {
		slices.Sort(names)
		q.Rule = "push " + repo + " " + strings.Join(names, ",")
	}
	return &q
}

// pushName names a push in one line for the log.
func pushName(repo string, commands []command) string {
	var refs []string
	for _, c := range commands {
		refs = append(refs, strings.TrimPrefix(c.ref, "refs/heads/"))
	}
	return "Push to " + repo + ": " + strings.Join(refs, ", ")
}

// hold asks the user about a push. It returns true when a rule allows it
// and the push goes on. Otherwise it keeps the push, refuses it for now so
// git does not wait, and sends it to GitHub once the user allows it.
func (p *Proxy) hold(w http.ResponseWriter, r *http.Request, owner, repo string, body io.Reader, commands []command, caps string, q approve.Question) bool {
	data, err := io.ReadAll(io.LimitReader(body, maxHeld+1))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return false
	}
	if len(data) > maxHeld {
		refuse(w, commands, caps, fmt.Errorf("the push is over %d MB, more than den keeps while the user decides; ask the user to push it themselves", maxHeld>>20))
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(data))

	turn := ""
	if p.Turn != nil {
		turn = p.Turn()
	}
	header := r.Header.Clone()
	waiting, err := p.later(q, func(answer error) {
		p.pushLater(owner, repo, header, data, commands, q.What, turn, answer)
	})
	switch {
	case err != nil:
		refuse(w, commands, caps, err)
		return false
	case waiting:
		refuse(w, commands, caps, errWaiting)
		return false
	}
	return true
}

var errWaiting = errors.New("waiting for the user in Telegram; den pushes it once they allow it and tells you, so do not push it again")

// watched passes a response on and hands what it read to done once, up to
// 1 MB, when it ends.
type watched struct {
	io.ReadCloser
	kept bytes.Buffer
	done func([]byte)
	once sync.Once
}

func (w *watched) Read(b []byte) (int, error) {
	n, err := w.ReadCloser.Read(b)
	if w.kept.Len() < 1<<20 {
		w.kept.Write(b[:n])
	}
	if err == io.EOF {
		w.once.Do(func() { w.done(w.kept.Bytes()) })
	}
	return n, err
}

func (w *watched) Close() error {
	w.once.Do(func() { w.done(w.kept.Bytes()) })
	return w.ReadCloser.Close()
}

// record remembers the branches a push created and forgets the ones it
// deleted, of the refs GitHub took.
func (p *Proxy) record(repo string, commands []command, took map[string]bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range commands {
		branch, isBranch := strings.CutPrefix(c.ref, "refs/heads/")
		switch {
		case !isBranch || !took[c.ref]:
		case zero(c.new):
			delete(p.mine, repo+":"+branch)
		case zero(c.old):
			p.mine[repo+":"+branch] = true
		}
	}
	p.save()
}

// pushLater sends a push the user answered to GitHub, and reports what came
// of it. A branch that moved on GitHub in the meantime refuses the push, as
// it would refuse git itself.
func (p *Proxy) pushLater(owner, repo string, header http.Header, data []byte, commands []command, what, turn string, answer error) {
	report := func(agent, user string) {
		if p.Report != nil {
			p.Report(turn, agent, user)
		}
	}
	if answer != nil {
		log.Printf("git: %s: not pushed: %v", what, answer)
		if !errors.Is(answer, context.Canceled) {
			report(fmt.Sprintf("[den: %s was not pushed: %v.]", what, answer), "")
		}
		return
	}

	took, refused, err := p.replay(owner, repo, header, data)
	switch {
	case err != nil:
		log.Printf("git: %s: %v", what, err)
		report(fmt.Sprintf("[den: the user allowed %s, and it failed: %v.]", what, err), fmt.Sprintf("%s failed: %v", what, err))
	case refused != "":
		log.Printf("git: %s: refused: %s", what, refused)
		report(fmt.Sprintf("[den: the user allowed %s, and GitHub refused it: %s. When a branch moved meanwhile, fetch, rebase or merge, and push again.]", what, refused),
			fmt.Sprintf("%s: GitHub refused it: %s", what, refused))
	default:
		log.Printf("git: %s: pushed, allowed by the user", what)
		p.record(owner+"/"+repo, commands, took)
		report(fmt.Sprintf("[den: the user allowed %s, and den pushed it. git fetch updates your copy of the remote branches.]", what), "")
	}
}

// replay sends a kept push to GitHub and returns the refs it took and its
// reasons for the ones it refused, empty when it took them all.
func (p *Proxy) replay(owner, repo string, header http.Header, data []byte) (map[string]bool, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.upstream+"/"+owner+"/"+repo+".git/git-receive-pack", bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}
	for _, k := range []string{"Content-Type", "Content-Encoding", "Accept", "Git-Protocol", "User-Agent"} {
		if v := header.Get(k); v != "" {
			req.Header.Set(k, v)
		}
	}
	if token := p.token(); token != "" {
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token)))
	}

	client := &http.Client{Transport: p.transport}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		text, _ := io.ReadAll(io.LimitReader(resp.Body, 1000))
		return nil, "", fmt.Errorf("GitHub answered %d: %s", resp.StatusCode, strings.TrimSpace(string(text)))
	}
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, "", err
	}
	took, refused, err := readReport(out)
	return took, strings.Join(refused, "; "), err
}

// readReport reads receive-pack's report, with or without side bands, and
// returns the refs GitHub took and its reasons for the ones it refused.
func readReport(out []byte) (map[string]bool, []string, error) {
	lines, err := pktLines(out)
	if err != nil {
		return nil, nil, err
	}
	// With side bands, the report is itself pkt-lines inside band 1.
	if len(lines) > 0 && len(lines[0]) > 0 && lines[0][0] <= 3 {
		var report, errs []byte
		for _, l := range lines {
			switch {
			case len(l) == 0:
			case l[0] == 1:
				report = append(report, l[1:]...)
			case l[0] == 3:
				errs = append(errs, l[1:]...)
			}
		}
		if len(errs) > 0 {
			return nil, []string{strings.TrimSpace(string(errs))}, nil
		}
		if lines, err = pktLines(report); err != nil {
			return nil, nil, err
		}
	}

	took := map[string]bool{}
	var refused []string
	for _, l := range lines {
		line := strings.TrimSpace(string(l))
		switch {
		case strings.HasPrefix(line, "unpack ") && line != "unpack ok":
			refused = append(refused, line)
		case strings.HasPrefix(line, "ng "):
			refused = append(refused, strings.TrimPrefix(line, "ng "))
		case strings.HasPrefix(line, "ok "):
			took[strings.TrimPrefix(line, "ok ")] = true
		}
	}
	if len(refused) == 0 && len(took) == 0 {
		return nil, nil, errors.New("GitHub sent no report")
	}
	if len(refused) > 0 {
		// A failed unpack refuses every ref, whatever the lines say.
		for _, r := range refused {
			if strings.HasPrefix(r, "unpack ") {
				took = map[string]bool{}
			}
		}
	}
	return took, refused, nil
}

// pktLines splits git's pkt-line data, dropping flush packets.
func pktLines(data []byte) ([][]byte, error) {
	var lines [][]byte
	for len(data) >= 4 {
		n, err := strconv.ParseUint(string(data[:4]), 16, 16)
		if err != nil {
			return nil, fmt.Errorf("reading GitHub's report: %q is no length", data[:4])
		}
		if n < 4 {
			data = data[4:]
			continue
		}
		if int(n) > len(data) {
			return nil, errors.New("reading GitHub's report: short line")
		}
		lines = append(lines, data[4:n])
		data = data[n:]
	}
	return lines, nil
}

func (p *Proxy) save() {
	var list []string
	for b := range p.mine {
		list = append(list, b)
	}
	data, _ := json.Marshal(list)
	path := filepath.Join(p.cfg.State, branchesFile)
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		log.Printf("git: %v", err)
		return
	}
	os.Rename(path+".tmp", path)
}

func describe(c command) string {
	name := strings.TrimPrefix(c.ref, "refs/heads/")
	switch {
	case zero(c.new):
		return "• delete " + name
	case zero(c.old):
		return "• create " + name + " at " + c.new[:7]
	}
	return "• " + name + ": " + c.old[:7] + " → " + c.new[:7] + " (may rewrite its history)"
}

// readCommands reads the refs a push changes from the start of its body,
// and returns a body that still holds every byte for GitHub.
func readCommands(r *http.Request) (io.Reader, []command, string, error) {
	var raw io.Reader = r.Body
	var source *bufio.Reader
	var kept bytes.Buffer

	if r.Header.Get("Content-Encoding") == "gzip" {
		// git compresses only small bodies.
		data, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
		if err != nil {
			return nil, nil, "", err
		}
		raw = bytes.NewReader(data)
		z, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, nil, "", err
		}
		source = bufio.NewReader(z)
	} else {
		source = bufio.NewReader(io.TeeReader(r.Body, &kept))
	}

	var commands []command
	var caps string
	for {
		var size [4]byte
		if _, err := io.ReadFull(source, size[:]); err != nil {
			if err == io.EOF {
				break
			}
			return nil, nil, "", fmt.Errorf("reading the push: %v", err)
		}
		n, err := strconv.ParseUint(string(size[:]), 16, 16)
		if err != nil {
			return nil, nil, "", fmt.Errorf("reading the push: %q is no length", size)
		}
		if n == 0 {
			break
		}
		if n < 4 {
			return nil, nil, "", errors.New("reading the push: short line")
		}
		line := make([]byte, n-4)
		if _, err := io.ReadFull(source, line); err != nil {
			return nil, nil, "", fmt.Errorf("reading the push: %v", err)
		}
		text, more, found := strings.Cut(strings.TrimSuffix(string(line), "\n"), "\x00")
		if found {
			caps = more
		}
		if strings.HasPrefix(text, "shallow ") {
			continue
		}
		// Every other line must be a command den understands, read the way
		// git reads it, so none reaches GitHub unseen.
		c, ok := parseCommand(text)
		if !ok {
			return nil, nil, "", fmt.Errorf("den does not pass on this push: it cannot read %q", text)
		}
		commands = append(commands, c)
	}

	if r.Header.Get("Content-Encoding") == "gzip" {
		return raw, commands, caps, nil
	}
	// kept holds every byte the reader took in, read or buffered; the rest
	// is still in the body.
	return io.MultiReader(&kept, r.Body), commands, caps, nil
}

// parseCommand reads "<old> <new> <ref>" as git does: two object IDs and
// the rest of the line. den takes only refs of printable ASCII, so what the
// user reads in a question is what GitHub gets.
func parseCommand(text string) (command, bool) {
	old, rest, _ := strings.Cut(text, " ")
	new, ref, _ := strings.Cut(rest, " ")
	if !objectID(old) || !objectID(new) || !strings.HasPrefix(ref, "refs/") {
		return command{}, false
	}
	for _, r := range ref {
		if r <= ' ' || r > '~' {
			return command{}, false
		}
	}
	return command{old: old, new: new, ref: ref}, true
}

func objectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

// refuse answers a push the user did not allow the way GitHub refuses one,
// so git names each branch and den's reason.
func refuse(w http.ResponseWriter, commands []command, caps string, why error) {
	reason := why.Error()
	switch {
	case errors.Is(why, approve.ErrDenied):
		reason = "the user did not allow this push; do not push it again"
	case errors.Is(why, approve.ErrExpired):
		reason = "the user did not answer in time"
	}

	var report bytes.Buffer
	report.WriteString(pkt("unpack ok\n"))
	for _, c := range commands {
		report.WriteString(pkt("ng " + c.ref + " den: " + reason + "\n"))
	}
	report.WriteString("0000")

	w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
	w.Header().Set("Cache-Control", "no-cache")
	if !strings.Contains(caps, "side-band") {
		w.Write(report.Bytes())
		return
	}
	io.WriteString(w, pkt("\x02den: "+reason+"\n"))
	data := report.Bytes()
	for len(data) > 0 {
		n := min(len(data), 65000)
		io.WriteString(w, pkt("\x01"+string(data[:n])))
		data = data[n:]
	}
	io.WriteString(w, "0000")
}

func pkt(s string) string {
	return fmt.Sprintf("%04x%s", len(s)+4, s)
}
