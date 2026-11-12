package gitproxy

import (
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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
	var auth []string
	backend := &cgi.Handler{Path: gitPath, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + filepath.Join(root, "github"), "GIT_HTTP_EXPORT_ALL=1"}}
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
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
	p.Turn = func() string { return "t1" }
	p.Report = func(turn, agent, user string) { reports = append(reports, turn+" "+agent) }
	p.upstream = github.URL
	proxy := httptest.NewServer(p)
	defer proxy.Close()

	work := filepath.Join(root, "work")
	must(root, "clone", proxy.URL+"/me/repo", work)
	if auth[0] != "Basic eC1hY2Nlc3MtdG9rZW46dG9r" {
		t.Fatalf("authorization %q", auth[0])
	}

	// A new branch, and later changes to it, go at once.
	must(work, "commit", "--allow-empty", "-m", "second")
	must(work, "push", "origin", "HEAD:refs/heads/feature")
	must(work, "commit", "--allow-empty", "-m", "third")
	must(work, "push", "origin", "HEAD:refs/heads/feature")
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
		"t1 [den: Push to me/repo: main was not pushed: the user did not allow it.]",
		"t1 [den: the user allowed Push to me/repo: main, and den pushed it. git fetch updates your copy of the remote branches.]",
	}
	if len(reports) != 3 || reports[0] != want[0] || reports[1] != want[1] || !strings.Contains(reports[2], "GitHub refused it: refs/heads/main") {
		t.Fatalf("reports %q", reports)
	}

	// The agent's own branch goes away without a question, and den
	// remembers its branches across restarts.
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
