package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/unsady/den/internal/store"
)

type fakeChat struct {
	mu     sync.Mutex
	posted []string
	effort string
}

func (f *fakeChat) Post(_ context.Context, text, session string, files []chat.File) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posted = append(f.posted, text+session)
	for _, file := range files {
		f.posted = append(f.posted, file.Name+":"+string(file.Data))
	}
	return nil
}

func (f *fakeChat) Start(_ context.Context, text string, files []chat.File) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posted = append(f.posted, "new:"+text)
	return "s1", nil
}

func (f *fakeChat) Stop(context.Context) bool { return false }

func (f *fakeChat) Open(context.Context, string) error { return nil }

func (f *fakeChat) Sessions(context.Context, string, *store.Mark, int) ([]store.Session, error) {
	return nil, nil
}

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
	var dropped []string
	drop := func(_ context.Context, id string) (int, error) {
		if id != "s1" {
			return http.StatusNotFound, errors.New("no session " + id)
		}
		dropped = append(dropped, id)
		return http.StatusOK, nil
	}
	s := New(cfg, c, a, drop, hub)
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
	if status, body := call("GET", "/setup", token, ""); status != http.StatusOK || !strings.Contains(body, `"name":"M"`) || !strings.Contains(body, `"api":1`) {
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
	if status, _ := call("POST", "/messages", token, `{"text":"","files":[{"name":"a.png","data":"aGk="}]}`); status != http.StatusOK || strings.Join(c.posted, "|") != "Plan my week||a.png:hi" {
		t.Fatalf("file: %d %v", status, c.posted)
	}
	if status, body := call("POST", "/messages", token, `{"text":"Plan my trip","new":true}`); status != http.StatusOK || !strings.Contains(body, `"session":"s1"`) || c.posted[len(c.posted)-1] != "new:Plan my trip" {
		t.Fatalf("new: %d %s %v", status, body, c.posted)
	}
	if status, _ := call("POST", "/messages", token, `{"text":"hi","new":true,"session":"s1"}`); status != http.StatusBadRequest {
		t.Fatalf("new with a session: %d", status)
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

	// Asked for pages, a session gives its latest messages first, each page
	// with where the one before it ends.
	hub.Publish(events.Event{Type: events.Reply, Session: "s1", Text: "Two"})
	hub.Publish(events.Event{Type: events.Reply, Session: "s1", Text: "Three"})
	var page struct {
		Messages []struct{ Text string }
		Before   string
	}
	_, body := call("GET", "/sessions/s1?limit=2", token, "")
	if err := json.Unmarshal([]byte(body), &page); err != nil || len(page.Messages) != 2 || page.Messages[0].Text != "Two" || page.Before != "1" {
		t.Fatalf("latest page: %s", body)
	}
	page.Before = ""
	_, body = call("GET", "/sessions/s1?limit=2&before=1", token, "")
	if err := json.Unmarshal([]byte(body), &page); err != nil || len(page.Messages) != 1 || page.Messages[0].Text != "Hi" || page.Before != "" {
		t.Fatalf("first page: %s", body)
	}
	if status, _ := call("GET", "/sessions/s1?limit=x", token, ""); status != http.StatusBadRequest {
		t.Fatalf("bad limit: %d", status)
	}

	if status, _ := call("DELETE", "/sessions/s1", token, ""); status != http.StatusOK || strings.Join(dropped, "|") != "s1" {
		t.Fatalf("delete: %d %v", status, dropped)
	}
	if status, _ := call("DELETE", "/sessions/missing", token, ""); status != http.StatusNotFound {
		t.Fatalf("delete missing: %d", status)
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

	// A run going on shows in the setup of an app that comes back, and is
	// gone once it ends.
	waitSetup := func(want string, has bool) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for {
			if _, body := call("GET", "/setup", token, ""); strings.Contains(body, want) == has {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("setup has %q: %v", want, !has)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	hub.Publish(events.Event{Type: events.Run, Session: "s1", Text: "flash · 3 of 9"})
	waitSetup(`"runs":{"s1":"flash · 3 of 9"}`, true)
	hub.Publish(events.Event{Type: events.Run, Session: "s1"})
	waitSetup(`"runs":{}`, true)

	// Another device pairs beside the first, and unpairing shuts both out.
	again, _ := Pair(cfg)
	for _, device := range []string{token, again} {
		if status, _ := call("GET", "/setup", device, ""); status != http.StatusOK {
			t.Fatalf("paired device: %d", status)
		}
	}
	if err := Unpair(cfg); err != nil {
		t.Fatal(err)
	}
	for _, device := range []string{token, again} {
		if status, _ := call("GET", "/setup", device, ""); status != http.StatusUnauthorized {
			t.Fatalf("after unpair: %d", status)
		}
	}
}
