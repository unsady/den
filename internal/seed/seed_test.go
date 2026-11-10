package seed

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsady/den/internal/config"
)

func TestInitLinksBuiltinSkills(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		Space:    filepath.Join(dir, "space"),
		Archive:  filepath.Join(dir, "archive"),
		Skills:   filepath.Join(dir, "builtin"),
		FXSkills: filepath.Join(dir, "fx-skills"),
	}
	if err := Init(cfg); err != nil {
		t.Fatal(err)
	}

	// An old space holds a copy of a shipped skill and one the agent changed.
	old := "old den skill\n"
	sum := sha256.Sum256([]byte(old))
	shipped["den"] = append(shipped["den"], hex.EncodeToString(sum[:]))
	write(t, filepath.Join(cfg.Space, ".fx/skills/den/SKILL.md"), old)
	write(t, filepath.Join(cfg.Space, ".fx/skills/reflect/SKILL.md"), "changed by the agent\n")
	git(t, cfg.Space, "add", "-A")
	git(t, cfg.Space, "commit", "-q", "-m", "old copies")
	write(t, filepath.Join(cfg.Space, "notes/draft.md"), "uncommitted\n")

	write(t, filepath.Join(cfg.Skills, "web/SKILL.md"), "web\n")
	write(t, filepath.Join(cfg.FXSkills, "own/SKILL.md"), "the agent's own\n")
	if err := os.Symlink(filepath.Join(cfg.Skills, "gone"), filepath.Join(cfg.FXSkills, "gone")); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := Init(cfg); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := os.Stat(filepath.Join(cfg.Space, ".fx/skills/den")); !os.IsNotExist(err) {
		t.Fatalf("the shipped copy is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Space, ".fx/skills/reflect/SKILL.md")); err != nil {
		t.Fatalf("the changed copy is gone: %v", err)
	}
	if status := git(t, cfg.Space, "status", "--porcelain"); status != "?? notes/draft.md" {
		t.Fatalf("status %q", status)
	}

	if have, _ := os.Readlink(filepath.Join(cfg.FXSkills, "web")); have != filepath.Join(cfg.Skills, "web") {
		t.Fatalf("web links to %q", have)
	}
	if _, err := os.Lstat(filepath.Join(cfg.FXSkills, "gone")); !os.IsNotExist(err) {
		t.Fatalf("the stale link is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.FXSkills, "own/SKILL.md")); err != nil {
		t.Fatalf("the agent's skill is gone: %v", err)
	}
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
