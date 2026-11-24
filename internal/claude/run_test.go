package claude

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/unsady/den/internal/config"
)

// serve answers every download with body and counts the requests.
func serve(t *testing.T, body string) *int {
	t.Helper()
	n := new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*n++
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	old := Releases
	Releases = srv.URL
	t.Cleanup(func() { Releases = old })
	return n
}

func pin(t *testing.T, body string) {
	t.Helper()
	sum := sha256.Sum256([]byte(body))
	old := sums[runtime.GOARCH]
	sums[runtime.GOARCH] = hex.EncodeToString(sum[:])
	t.Cleanup(func() { sums[runtime.GOARCH] = old })
}

func withState(t *testing.T) *Claude {
	t.Helper()
	c, _ := newClaude(t)
	c.cfg.State = t.TempDir()
	return c
}

func TestInstall(t *testing.T) {
	c := withState(t)
	pin(t, "binary")
	requests := serve(t, "binary")

	root := filepath.Join(c.cfg.State, "claude")
	os.MkdirAll(filepath.Join(root, "2.0.0"), 0o700)
	os.MkdirAll(filepath.Join(root, "config"), 0o700)

	if err := c.Install(t.Context()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(Path(c.cfg))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("mode = %v, want 0700", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(root, "2.0.0")); err == nil {
		t.Error("the old version stayed")
	}
	if _, err := os.Stat(filepath.Join(root, "config")); err != nil {
		t.Error("config was removed")
	}

	if err := c.Install(t.Context()); err != nil {
		t.Fatal(err)
	}
	if *requests != 1 {
		t.Errorf("requests = %d, want 1", *requests)
	}
}

func TestInstallBadSum(t *testing.T) {
	c := withState(t)
	pin(t, "binary")
	serve(t, "something else")

	if err := c.Install(t.Context()); err == nil {
		t.Fatal("a wrong download was accepted")
	}
	for _, p := range []string{Path(c.cfg), Path(c.cfg) + ".part"} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s stayed", p)
		}
	}
}

func TestEnv(t *testing.T) {
	t.Setenv("DEN_SENTINEL", "leak")
	c := withState(t)

	env := strings.Join(c.Env("sk-ant-oat01-x"), "\n")
	for _, want := range []string{
		"CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat01-x",
		"CLAUDE_CONFIG_DIR=" + ConfigDir(c.cfg),
		"MCP_TOOL_TIMEOUT=86400000",
		"DISABLE_AUTOUPDATER=1",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %s", want)
		}
	}
	if strings.Contains(env, "DEN_SENTINEL") {
		t.Error("den's environment leaked")
	}
}

func TestCheck(t *testing.T) {
	cases := []struct {
		name, output, want string
	}{
		{"signed out", `{"is_error": true, "result": "Not logged in · Please run /login"}`, "Not logged in"},
		{"ok", `{"is_error": false, "result": "OK"}`, ""},
		{"not JSON", `hello`, "without JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := withState(t)
			if err := c.Save("sk-ant-oat01-x"); err != nil {
				t.Fatal(err)
			}
			c.bin = filepath.Join(t.TempDir(), "claude")
			os.WriteFile(c.bin, []byte("#!/bin/sh\ncat <<'EOF'\n"+tc.output+"\nEOF\n"), 0o700)

			err := c.Check(t.Context())
			switch {
			case tc.want == "" && err != nil:
				t.Fatal(err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestTranscripts(t *testing.T) {
	cfg := config.Config{State: t.TempDir()}
	c := New(cfg)
	project := filepath.Join(ConfigDir(cfg), "projects", "-var-lib-den-claude-work")
	for _, f := range []string{"a.jsonl", "a/tool-results/x.txt", "b.jsonl"} {
		path := filepath.Join(project, f)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	c.forget("a")
	c.forget("../b")
	if _, err := os.Stat(filepath.Join(project, "a.jsonl")); !os.IsNotExist(err) {
		t.Errorf("a.jsonl is kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, "a")); !os.IsNotExist(err) {
		t.Errorf("a's folder is kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, "b.jsonl")); err != nil {
		t.Errorf("b.jsonl is gone: %v", err)
	}

	if err := Sweep(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ConfigDir(cfg), "projects")); !os.IsNotExist(err) {
		t.Errorf("projects is kept: %v", err)
	}
}
