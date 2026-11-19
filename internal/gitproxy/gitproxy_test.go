package gitproxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/unsady/den/internal/approve"
	"github.com/unsady/den/internal/config"
)

// TestPush clones and pushes with real git through the proxy to git's own
// HTTP server, which stands in for GitHub.
func TestPush(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	run := func(dir string, args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "HOME="+root,
			"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@a", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@a")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(dir string, args ...string) {
		t.Helper()
		if out, err := run(dir, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// GitHub: a bare repository with one commit on main.
	bare := filepath.Join(root, "github", "me", "repo.git")
	must(root, "init", "--bare", "-b", "main", bare)
	must(bare, "config", "http.receivepack", "true")
	seed := filepath.Join(root, "seed")
	must(root, "init", "-b", "main", seed)
	must(seed, "commit", "--allow-empty", "-m", "first")
	must(seed, "push", bare, "main")

	var mu sync.Mutex
	var auth, sessions []string
	backend := &cgi.Handler{Path: gitPath, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + filepath.Join(root, "github"), "GIT_HTTP_EXPORT_ALL=1"}}
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
		sessions = append(sessions, r.Header.Get(SessionHeader))
		mu.Unlock()
		backend.ServeHTTP(w, r)
	}))
	defer github.Close()

	cfg := config.Config{State: filepath.Join(root, "state"), Secrets: filepath.Join(root, "secrets")}
	os.MkdirAll(cfg.State, 0o700)
	os.MkdirAll(cfg.Secrets, 0o700)
	os.WriteFile(filepath.Join(cfg.Secrets, TokenFile), []byte("tok\n"), 0o600)

	var asked, reports []string
	var then func(error)
	p := New(cfg, func(q approve.Question, answer func(error)) (bool, error) {
		asked = append(asked, q.Text)
		then = answer
		return true, nil
	})
	p.Known = func(session string) bool { return session == "s1" }
	p.Report = func(session, agent, user string) { reports = append(reports, session+" "+agent) }
	p.upstream = github.URL
	// Without GitHub's API, den keeps a push itself and asks by commit IDs.
	p.api = ""
	proxy := httptest.NewServer(p)
	defer proxy.Close()

	work := filepath.Join(root, "work")
	must(root, "clone", proxy.URL+"/me/repo", work)
	// den sets this for the agent's git, as the agent's session.
	must(work, "config", "http.extraHeader", SessionHeader+": s1")
	mu.Lock()
	first := auth[0]
	mu.Unlock()
	if first != "Basic eC1hY2Nlc3MtdG9rZW46dG9r" {
		t.Fatalf("authorization %q", first)
	}

	// A new branch, and later changes to it, go at once.
	must(work, "commit", "--allow-empty", "-m", "second")
	must(work, "push", "origin", "HEAD:refs/heads/feature")
	must(work, "commit", "--allow-empty", "-m", "third")
	must(work, "push", "origin", "HEAD:refs/heads/feature")
	if len(asked) != 0 {
		t.Fatalf("asked %q", asked)
	}

	// A push that waits for the user needs a session den has, to tell it
	// what came of the push.
	for _, header := range []string{"", SessionHeader + ": gone"} {
		out, err := run(work, "-c", "http.extraHeader=", "-c", "http.extraHeader="+header, "push", "origin", "HEAD:main")
		if err == nil || !strings.Contains(out, "den cannot tell which session pushed") {
			t.Fatalf("push from session %q: %v\n%s", header, err, out)
		}
	}
	if len(asked) != 0 {
		t.Fatalf("asked %q", asked)
	}

	// main is not the agent's: den keeps the push and refuses it for now,
	// and the user denies it.
	out, err := run(work, "push", "origin", "HEAD:main")
	if err == nil || !strings.Contains(out, "den: waiting for the user in Telegram") {
		t.Fatalf("held push: %v\n%s", err, out)
	}
	if len(asked) != 1 || !strings.HasPrefix(asked[0], "⇡ Push to me/repo\n\n• main: ") {
		t.Fatalf("asked %q", asked)
	}
	then(approve.ErrDenied)
	if head, _ := run(bare, "log", "-1", "--format=%s", "main"); strings.TrimSpace(head) != "first" {
		t.Fatalf("main moved to %q", head)
	}

	// Asked again, the user allows it, and den sends the kept push.
	run(work, "push", "origin", "HEAD:main")
	then(nil)
	if head, _ := run(bare, "log", "-1", "--format=%s", "main"); strings.TrimSpace(head) != "third" {
		t.Fatalf("main is at %q", head)
	}

	// A branch that moved while the push waited refuses it.
	must(seed, "fetch", bare, "main")
	must(seed, "reset", "--hard", "FETCH_HEAD")
	must(work, "commit", "--allow-empty", "-m", "fourth")
	run(work, "push", "origin", "HEAD:main")
	must(seed, "commit", "--allow-empty", "-m", "elsewhere")
	must(seed, "push", bare, "main")
	then(nil)
	if head, _ := run(bare, "log", "-1", "--format=%s", "main"); strings.TrimSpace(head) != "elsewhere" {
		t.Fatalf("main is at %q", head)
	}
	want := []string{
		"s1 [den: Push to me/repo: main was not pushed: the user did not allow it.]",
		"s1 [den: the user allowed Push to me/repo: main, and den pushed it. git fetch updates your copy of the remote branches.]",
	}
	if len(reports) != 3 || reports[0] != want[0] || reports[1] != want[1] || !strings.Contains(reports[2], "GitHub refused it: refs/heads/main") {
		t.Fatalf("reports %q", reports)
	}

	// The agent's own branch goes away without a question, and den
	// remembers its branches across restarts.
	mu.Lock()
	leaked := slices.ContainsFunc(sessions, func(s string) bool { return s != "" })
	mu.Unlock()
	if leaked {
		t.Fatal("GitHub got the session")
	}
	if !New(cfg, nil).mine["me/repo:feature"] {
		t.Fatal("forgot the agent's branch")
	}
	must(work, "push", "origin", ":feature")
	if len(asked) != 3 {
		t.Fatalf("asked %q", asked)
	}

	// A push that only claims to create main, and which GitHub refuses,
	// does not make main the agent's.
	zero := strings.Repeat("0", 40)
	fake := pkt(zero+" "+strings.Repeat("a", 40)+" refs/heads/main\x00report-status\n") + "0000"
	resp, err := http.Post(proxy.URL+"/me/repo/git-receive-pack", "application/x-git-receive-pack-request", strings.NewReader(fake))
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()
	if New(cfg, nil).mine["me/repo:main"] || p.mine["me/repo:main"] {
		t.Fatal("a refused push made main the agent's")
	}

	// A command den cannot read stops the push.
	odd := pkt(zero+" "+strings.Repeat("a", 40)+" refs/tags/v9\u00a0x\x00report-status\n") + "0000"
	resp, _ = http.Post(proxy.URL+"/me/repo/git-receive-pack", "application/x-git-receive-pack-request", strings.NewReader(odd))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("an unreadable command went on: %d", resp.StatusCode)
	}

	resp, _ = http.Get(proxy.URL + "/me/repo/other")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("other path: %d", resp.StatusCode)
	}
}

// TestHeldPush keeps a push that waits under den's own refs, names its
// commits in the question, and moves the branch once the user allows it.
func TestHeldPush(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	run := func(dir string, args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "HOME="+root,
			"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@a", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@a")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(dir string, args ...string) string {
		t.Helper()
		out, err := run(dir, args...)
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(out)
	}

	bare := filepath.Join(root, "github", "me", "repo.git")
	must(root, "init", "--bare", "-b", "main", bare)
	must(bare, "config", "http.receivepack", "true")
	seed := filepath.Join(root, "seed")
	must(root, "init", "-b", "main", seed)
	must(seed, "commit", "--allow-empty", "-m", "first")
	must(seed, "push", bare, "main")

	backend := &cgi.Handler{Path: gitPath, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + filepath.Join(root, "github"), "GIT_HTTP_EXPORT_ALL=1"}}
	github := httptest.NewServer(backend)
	defer github.Close()

	// GitHub's API, as far as den uses it, over the same repository.
	var mu sync.Mutex
	var calls []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+strings.TrimPrefix(r.URL.Path, "/repos/me/repo"))
		mu.Unlock()
		path := strings.TrimPrefix(r.URL.Path, "/repos/me/repo")
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/compare/"):
			io.WriteString(w, `{"status":"ahead","ahead_by":1,"behind_by":0,"commits":[{"commit":{"message":"third\n\nbody"}}],"files":[{"filename":"a.txt"}]}`)
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/git/ref/heads/"):
			sha, _ := run(bare, "rev-parse", "refs/heads/"+strings.TrimPrefix(path, "/git/ref/heads/"))
			io.WriteString(w, `{"object":{"sha":"`+strings.TrimSpace(sha)+`"}}`)
		case r.Method == http.MethodPatch && strings.HasPrefix(path, "/git/refs/heads/"):
			var body struct{ SHA string }
			json.NewDecoder(r.Body).Decode(&body)
			run(bare, "update-ref", "refs/heads/"+strings.TrimPrefix(path, "/git/refs/heads/"), body.SHA)
			io.WriteString(w, "{}")
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/git/refs/"):
			run(bare, "update-ref", "-d", strings.TrimPrefix(path, "/git/"))
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, `{"message":"unexpected"}`, http.StatusNotFound)
		}
	}))
	defer api.Close()

	cfg := config.Config{State: filepath.Join(root, "state"), Secrets: filepath.Join(root, "secrets")}
	os.MkdirAll(cfg.State, 0o700)
	os.MkdirAll(cfg.Secrets, 0o700)
	os.WriteFile(filepath.Join(cfg.Secrets, TokenFile), []byte("tok\n"), 0o600)

	var asked, reports []string
	var then func(error)
	p := New(cfg, func(q approve.Question, answer func(error)) (bool, error) {
		asked = append(asked, q.Text)
		then = answer
		return true, nil
	})
	p.Report = func(session, agent, user string) { reports = append(reports, agent) }
	p.Known = func(session string) bool { return session == "s1" }
	p.upstream = github.URL
	p.api = api.URL
	proxy := httptest.NewServer(p)
	defer proxy.Close()

	work := filepath.Join(root, "work")
	must(root, "clone", proxy.URL+"/me/repo", work)
	must(work, "config", "http.extraHeader", SessionHeader+": s1")
	must(work, "commit", "--allow-empty", "-m", "third")
	third := must(work, "rev-parse", "HEAD")

	out, err := run(work, "push", "origin", "HEAD:main")
	if err == nil || !strings.Contains(out, "waiting for the user") {
		t.Fatalf("held push: %v\n%s", err, out)
	}
	if len(asked) != 1 || !strings.Contains(asked[0], "• main: 1 commit, 1 file\n   third\n") {
		t.Fatalf("asked %q", asked)
	}
	// GitHub holds the commit under den's ref, and main has not moved.
	if refs := must(bare, "for-each-ref", "refs/den/"); !strings.Contains(refs, third) {
		t.Fatalf("no held ref: %q", refs)
	}
	if head := must(bare, "log", "-1", "--format=%s", "main"); head != "first" {
		t.Fatalf("main moved to %q", head)
	}

	then(nil)
	if head := must(bare, "log", "-1", "--format=%s", "main"); head != "third" {
		t.Fatalf("main is at %q", head)
	}
	if refs := must(bare, "for-each-ref", "refs/den/"); refs != "" {
		t.Fatalf("den's refs stay: %q", refs)
	}
	if len(reports) != 1 || !strings.Contains(reports[0], "den pushed it") {
		t.Fatalf("reports %q", reports)
	}

	// A denied push leaves nothing behind.
	must(work, "commit", "--allow-empty", "-m", "fourth")
	run(work, "push", "origin", "HEAD:main")
	then(approve.ErrDenied)
	if head := must(bare, "log", "-1", "--format=%s", "main"); head != "third" {
		t.Fatalf("main is at %q", head)
	}
	if refs := must(bare, "for-each-ref", "refs/den/"); refs != "" {
		t.Fatalf("den's refs stay: %q", refs)
	}
}
