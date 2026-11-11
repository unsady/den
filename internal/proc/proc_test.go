package proc

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/unsady/den/internal/config"
)

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// TestOutOfMemory needs a Linux container with a small memory limit:
//
//	GOOS=linux go test -c ./internal/proc
//	docker run --rm --memory 512m -e DEN_OOM_TEST=1 -v $PWD:/work den /work/proc.test
func TestOutOfMemory(t *testing.T) {
	if os.Getenv("DEN_OOM_TEST") == "" {
		t.Skip("set DEN_OOM_TEST=1 in a container with a small memory limit")
	}

	// tail keeps the whole last line, and a stream of zeros has no newline.
	before := OOMKills()
	cmd := exec.Command("tail")
	cmd.Stdin = io.LimitReader(zeros{}, 8<<30)
	err := cmd.Run()
	if !OutOfMemory(err, before) {
		t.Fatalf("tail ended with %v and %d OOM kills before, %d after; want an OOM kill", err, before, OOMKills())
	}
	t.Logf("limit %s, tail: %v", MemoryLimit(), err)
}

func TestWriteFileReplacesALink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "secret")
	os.WriteFile(target, []byte("keep"), 0o600)
	path := filepath.Join(dir, "home", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.Symlink(target, path)

	if err := WriteFile(config.Config{}, path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(target); string(data) != "keep" {
		t.Fatalf("the link's target became %q", data)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("path is %v, %v", info.Mode(), err)
	}
	if data, err := ReadFile(config.Config{}, path); err != nil || string(data) != "new" {
		t.Fatalf("read %q, %v", data, err)
	}

	// A new file goes in a folder den makes as the agent.
	if err := WriteFile(config.Config{}, filepath.Join(dir, "fresh", "AGENTS.md"), []byte("rules"), 0o644); err != nil {
		t.Fatal(err)
	}
}
