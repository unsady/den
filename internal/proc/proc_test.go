package proc

import (
	"io"
	"os"
	"os/exec"
	"testing"
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
