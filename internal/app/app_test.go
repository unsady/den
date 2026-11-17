package app

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/unsady/den/internal/approve"
	"github.com/unsady/den/internal/chat"
	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/events"
)

type fakeChat struct {
	mu     sync.Mutex
	posted []string
	effort string
}

func (f *fakeChat) Post(_ context.Context, text, session string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posted = append(f.posted, text+session)
}

func (f *fakeChat) Stop(context.Context) bool { return false }

func (f *fakeChat) Open(context.Context, string) error { return nil }

func (f *fakeChat) Setup(context.Context) (chat.Setup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return chat.Setup{Model: chat.Model{Provider: "p", Model: "m", Name: "M", Effort: f.effort, Efforts: []string{"low", "high"}}}, nil
}

func (f *fakeChat) Models(context.Context, string) ([]chat.Model, error) { return nil, nil }

func (f *fakeChat) ChooseModel(context.Context, string, string) error { return nil }

func (f *fakeChat) ChooseEffort(_ context.Context, level string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.effort = level
	return nil
}

func (f *fakeChat) ChooseSecond(context.Context, string, string) error { return nil }

func (f *fakeChat) ChooseSecondEffort(context.Context, string) error { return nil }

type fakeAsker struct{ answers []string }

func (f *fakeAsker) Pending() []events.Event {
	return []events.Event{{Type: events.Ask, ID: "q1", What: "Send mail"}}
}

func (f *fakeAsker) Answer(id, outcome string) (string, bool) {
	if id != "q1" {
		return "", false
	}
	f.answers = append(f.answers, outcome)
	return "Send mail\n✓ Allowed", true
}

func TestServer(t *testing.T) {
	cfg := config.Config{Secrets: t.TempDir(), Sessions: t.TempDir()}
	hub := &events.Hub{Dir: t.TempDir()}
	c, a := &fakeChat{}, &fakeAsker{}
	s := New(cfg, c, a, hub)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.keep(ctx)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	call := func(method, path, token, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out strings.Builder
		bufio.NewReader(resp.Body).WriteTo(&out)
		return resp.StatusCode, strings.TrimSpace(out.String())
	}

	// Before pairing, nothing gets in.
	if status, _ := call("GET", "/setup", "anything", ""); status != http.StatusUnauthorized {
		t.Fatalf("unpaired: %d", status)
	}

	token, err := Pair(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(cfg.Secrets, tokenFile)); strings.Contains(string(data), token) {
		t.Fatal("the token is kept as it is")
	}
	if status, _ := call("GET", "/setup", "", ""); status != http.StatusUnauthorized {
		t.Fatalf("no token: %d", status)
	}
	if status, _ := call("GET", "/setup", token+"x", ""); status != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", status)
	}
	if status, body := call("GET", "/setup", token, ""); status != http.StatusOK || !strings.Contains(body, `"name":"M"`) {
		t.Fatalf("setup: %d %s", status, body)
	}

	// The stream passes what happens.
	req, _ := http.NewRequest("GET", srv.URL+"/events", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	lines := bufio.NewScanner(resp.Body)
	lines.Scan()
	if lines.Text() != ": connected" {
		t.Fatalf("first line %q", lines.Text())
	}
	hub.Publish(events.Event{Type: events.Reply, Session: "s1", Text: "Hi"})
	for lines.Scan() && lines.Text() == "" {
	}
	var e events.Event
	if err := json.Unmarshal([]byte(strings.TrimPrefix(lines.Text(), "data: ")), &e); err != nil || e.Text != "Hi" || e.Session != "s1" {
		t.Fatalf("event %q: %v", lines.Text(), err)
	}

	if status, _ := call("POST", "/messages", token, `{"text":"  "}`); status != http.StatusBadRequest {
		t.Fatalf("empty message: %d", status)
	}
	if status, _ := call("POST", "/messages", token, `{"text":"Plan my week"}`); status != http.StatusOK || strings.Join(c.posted, "|") != "Plan my week" {
		t.Fatalf("message: %d %v", status, c.posted)
	}

	if status, body := call("PUT", "/effort", token, `{"effort":"high"}`); status != http.StatusOK || !strings.Contains(body, `"effort":"high"`) {
		t.Fatalf("effort: %d %s", status, body)
	}

	if status, _ := call("POST", "/questions/q1", token, `{"answer":"yes"}`); status != http.StatusBadRequest {
		t.Fatalf("unknown answer: %d", status)
	}
	if status, _ := call("POST", "/questions/q1", token, `{"answer":"allow"}`); status != http.StatusOK || a.answers[0] != approve.Allowed {
		t.Fatalf("answer: %d %v", status, a.answers)
	}
	if status, _ := call("POST", "/questions/q2", token, `{"answer":"deny"}`); status != http.StatusGone {
		t.Fatalf("gone question: %d", status)
	}

	if status, _ := call("GET", "/sessions/..", token, ""); status == http.StatusOK {
		t.Fatal("a path outside the sessions was read")
	}
	// A session shows what its journal kept, and one without a journal
	// nothing.
	if status, body := call("GET", "/sessions/s1", token, ""); status != http.StatusOK || !strings.Contains(body, `"text":"Hi"`) {
		t.Fatalf("session: %d %s", status, body)
	}
	if status, body := call("GET", "/sessions/missing", token, ""); status != http.StatusOK || strings.TrimSpace(body) != "[]" {
		t.Fatalf("missing session: %d %s", status, body)
	}

	// Results wait for an app that was away.
	hub.Publish(events.Event{Type: events.Result, Title: "⏰ morning", Text: "Three things"})
	deadline := time.Now().Add(time.Second)
	for {
		if _, body := call("GET", "/results", token, ""); strings.Contains(body, "Three things") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the result was not kept")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A new pairing shuts the old token out, and unpairing every token.
	again, _ := Pair(cfg)
	if status, _ := call("GET", "/setup", token, ""); status != http.StatusUnauthorized {
		t.Fatalf("old token: %d", status)
	}
	if err := Unpair(cfg); err != nil {
		t.Fatal(err)
	}
	if status, _ := call("GET", "/setup", again, ""); status != http.StatusUnauthorized {
		t.Fatalf("after unpair: %d", status)
	}
}
