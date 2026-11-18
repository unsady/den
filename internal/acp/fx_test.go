//go:build fxacp

package acp

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestRealFX talks to the real `fx acp --full-access`, the binary named by
// $FX or else fx on the PATH. It needs fx's login and the network. A model
// to select with SetModel comes from $FX_ACP_MODEL.
func TestRealFX(t *testing.T) {
	bin := os.Getenv("FX")
	if bin == "" {
		bin = "fx"
	}

	path, err := exec.LookPath(bin)
	if err != nil {
		t.Skipf("no fx: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	dir := t.TempDir()
	start := func() *Conn {
		cmd := exec.Command(path, "acp", "--full-access")
		cmd.Dir = dir
		cmd.Stderr = os.Stderr

		c, err := Start(ctx, cmd)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}

	c := start()
	defer c.Close()

	id, err := c.NewSession(ctx, dir, "")
	if err != nil {
		t.Fatal(err)
	}

	model := os.Getenv("FX_ACP_MODEL")
	if model != "" {
		err = c.SetModel(ctx, id, model, "")
		if err != nil {
			t.Fatal(err)
		}
	}

	var events int
	res, err := c.Prompt(ctx, id, Prompt{Text: "Reply with exactly: pong"}, func(Event) { events++ })
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("stop=%s text=%q in=%d out=%d events=%d", res.StopReason, res.Text, res.InputTokens, res.OutputTokens, events)
	if !strings.Contains(strings.ToLower(res.Text), "pong") {
		t.Errorf("reply %q has no pong", res.Text)
	}

	// A new process takes the session up again without replaying it.
	c.Close()
	c = start()
	defer c.Close()

	err = c.Resume(ctx, id, dir)
	if err != nil {
		t.Fatal(err)
	}
	if model != "" {
		err = c.SetModel(ctx, id, model, "")
		if err != nil {
			t.Fatal(err)
		}
	}

	var replayed []string
	res, err = c.Prompt(ctx, id, Prompt{Text: "Reply with exactly: ping"}, func(e Event) {
		if e.Kind == KindOther {
			replayed = append(replayed, string(e.Raw))
		}
		if e.Kind == KindText && strings.Contains(e.Text, "pong") {
			t.Errorf("replayed text %q", e.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("resumed: text=%q other events=%v", res.Text, replayed)
	if !strings.Contains(strings.ToLower(res.Text), "ping") {
		t.Errorf("resumed reply %q has no ping", res.Text)
	}
}

// TestRealFXChosenID starts a session under the id den chose: fx holds it
// until its first prompt, then a new process resumes it by that id.
func TestRealFXChosenID(t *testing.T) {
	bin := os.Getenv("FX")
	if bin == "" {
		bin = "fx"
	}

	path, err := exec.LookPath(bin)
	if err != nil {
		t.Skipf("no fx: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	dir := t.TempDir()
	start := func() *Conn {
		cmd := exec.Command(path, "acp", "--full-access")
		cmd.Dir = dir
		cmd.Stderr = os.Stderr

		c, err := Start(ctx, cmd)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	const id = "denChosen123"

	c := start()
	got, err := c.NewSession(ctx, dir, id)
	if err != nil || got != id {
		t.Fatalf("session %q: %v", got, err)
	}
	c.Close()

	// Before its first prompt, fx has not saved it.
	c = start()
	if err := c.Resume(ctx, id, dir); !NotFound(err) {
		t.Fatalf("resume before the first prompt: %v", err)
	}
	if _, err := c.NewSession(ctx, dir, id); err != nil {
		t.Fatal(err)
	}
	// The prompt saves the session whether or not the model answers.
	res, err := c.Prompt(ctx, id, Prompt{Text: "Reply with exactly: pong"}, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("stop=%s text=%q", res.StopReason, res.Text)
	c.Close()

	c = start()
	defer c.Close()
	if err := c.Resume(ctx, id, dir); err != nil {
		t.Fatalf("resume after the first prompt: %v", err)
	}
}
