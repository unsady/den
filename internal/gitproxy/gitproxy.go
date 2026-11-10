// Package gitproxy lets the agent clone from and push to GitHub with the
// user's token, which it never sees.
//
// den serves git's smart HTTP protocol on the container's loopback address,
// and git's url.insteadOf sends https://github.com/ there. den adds the
// token and passes each request on to github.com. Before a push goes on,
// den reads the branches it changes: a branch the agent created through den
// changes at once, and anything else waits until the user allows it in
// Telegram.
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

// Ask asks the user to allow an action and returns nil when they do.
type Ask func(ctx context.Context, q approve.Question) error

// paths are the requests of git's smart HTTP protocol for one repository.
var paths = regexp.MustCompile(`^/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?/(info/refs|git-upload-pack|git-receive-pack)$`)

type Proxy struct {
	cfg config.Config
	ask Ask
	// Note logs a push that went without a question.
	Note func(what, outcome string)
	// upstream is GitHub's address; tests change it.
	upstream  string
	transport http.RoundTripper

	mu sync.Mutex
	// mine are the branches the agent created, as owner/repo:branch.
	mine map[string]bool
}

func New(cfg config.Config, ask Ask) *Proxy {
	p := &Proxy{cfg: cfg, ask: ask, upstream: "https://github.com", mine: map[string]bool{}}
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

	if op == "git-receive-pack" && r.Method == http.MethodPost {
		body, commands, caps, err := readCommands(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(body)
		if err := p.review(r.Context(), owner+"/"+repo, commands); err != nil {
			io.Copy(io.Discard, body)
			refuse(w, commands, caps, err)
			return
		}
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

// review lets a push go on when it only creates branches, or changes ones
// the agent created, and asks the user about anything else.
func (p *Proxy) review(ctx context.Context, repo string, commands []command) error {
	var asked, refs, names []string
	// Changes to existing branches can be allowed for good; deleting a
	// branch or pushing a tag is asked every time.
	lasting := true
	for _, c := range commands {
		branch, isBranch := strings.CutPrefix(c.ref, "refs/heads/")
		refs = append(refs, branch)
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

	what := "Push to " + repo + ": " + strings.Join(refs, ", ")
	if len(asked) > 0 {
		q := approve.Question{Text: "Push to " + repo + "?\n\n" + strings.Join(asked, "\n"), What: what}
		if lasting {
			slices.Sort(names)
			q.Rule = "push " + repo + " " + strings.Join(names, ",")
		}
		if err := p.ask(ctx, q); err != nil {
			return err
		}
	} else if p.Note != nil {
		p.Note(what, approve.Went)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range commands {
		branch, isBranch := strings.CutPrefix(c.ref, "refs/heads/")
		switch {
		case !isBranch:
		case zero(c.new):
			delete(p.mine, repo+":"+branch)
		case zero(c.old):
			p.mine[repo+":"+branch] = true
		}
	}
	p.save()
	return nil
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
		fields := strings.Fields(text)
		if len(fields) == 3 && len(fields[0]) >= 40 && len(fields[1]) >= 40 {
			commands = append(commands, command{old: fields[0], new: fields[1], ref: fields[2]})
		}
	}

	if r.Header.Get("Content-Encoding") == "gzip" {
		return raw, commands, caps, nil
	}
	// kept holds every byte the reader took in, read or buffered; the rest
	// is still in the body.
	return io.MultiReader(&kept, r.Body), commands, caps, nil
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
