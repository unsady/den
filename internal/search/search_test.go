package search

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/unsady/den/internal/config"
)

// fake answers as each service does, or with the status set for it.
type fake struct {
	mu     sync.Mutex
	status map[string]int
	hits   map[string]int
	// limited makes Exa's free endpoint answer with its rate limit error.
	limited bool
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.Trim(r.URL.Path, "/")
	f.mu.Lock()
	f.hits[name]++
	code, limited := f.status[name], f.limited
	f.mu.Unlock()
	if code != 0 && code != 200 {
		w.WriteHeader(code)
		return
	}

	body, _ := io.ReadAll(r.Body)
	var req map[string]any
	_ = json.Unmarshal(body, &req)
	mcp := func(text string, isError bool) string {
		data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{
			"isError": isError, "content": []any{map[string]any{"type": "text", "text": text}},
		}})
		return string(data)
	}

	switch name {
	case "deepseek":
		if r.Header.Get("Authorization") != "Bearer d" || req["count"] != float64(3) || len([]rune(req["query"].(string))) != 128 {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"results":[{"title":"Go","url":"https://go.dev","snippet":"short","summary":"Go is a language"}]}`)
	case "tavily":
		if r.Header.Get("Authorization") != "Bearer t" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"results":[{"title":"Go","url":"https://go.dev","content":"Go is"}]}`)
	case "exa-free":
		if limited {
			_, _ = io.WriteString(w, "event: message\ndata: "+mcp("You've hit Exa's free MCP rate limit.", true)+"\n\n")
			return
		}
		text := "Title: Go\nURL: https://go.dev\nPublished: N/A\nHighlights:\nThe Go\nlanguage\n---\nTitle: Tour\nURL: https://go.dev/tour\nHighlights:\nLearn Go"
		_, _ = io.WriteString(w, "event: message\ndata: "+mcp(text, false)+"\n\n")
	case "keenable":
		if r.Header.Get("X-Keenable-Title") != "den" || req["query"] != "go" {
			t := "bad request"
			http.Error(w, t, http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"results":[{"title":"Go","url":"https://go.dev","snippet":"From Keenable"}]}`)
	case "parallel":
		_, _ = io.WriteString(w, mcp(`{"results":[{"title":"Go","url":"https://go.dev","excerpts":["one","two"]}]}`, false))
	}
}

func TestSearchRotates(t *testing.T) {
	f := &fake{status: map[string]int{}, hits: map[string]int{}}
	server := httptest.NewServer(f)
	defer server.Close()
	for name := range endpoints {
		endpoints[name] = server.URL + "/" + name
	}

	dir := t.TempDir()
	s := New(config.Config{Secrets: dir})
	now := time.Now()
	s.now = func() time.Time { return now }
	search := func(want string) Reply {
		t.Helper()
		reply, err := s.Search(context.Background(), "go", 3)
		if err != nil || reply.Service != want {
			t.Fatalf("got %+v %v, want %s", reply, err, want)
		}
		return reply
	}

	// Without keys, Exa's free endpoint answers.
	reply := search("exa-free")
	if len(reply.Results) != 2 || reply.Results[0].Snippet != "The Go language" || reply.Results[1].URL != "https://go.dev/tour" {
		t.Fatalf("exa results %+v", reply.Results)
	}

	// When it is rate limited, Keenable answers, and Exa rests.
	f.limited = true
	search("keenable")
	search("keenable")
	if f.hits["exa-free"] != 2 {
		t.Fatalf("hits %v", f.hits)
	}

	// A key goes first.
	if err := os.WriteFile(filepath.Join(dir, "tavily_key"), []byte("t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	search("tavily")

	// A rejected key rests for a day, and the free services answer meanwhile.
	f.status["tavily"] = http.StatusPaymentRequired
	f.status["keenable"] = http.StatusInternalServerError
	reply = search("parallel")
	if reply.Results[0].Snippet != "one two" {
		t.Fatalf("parallel results %+v", reply.Results)
	}
	now = now.Add(2 * time.Hour)
	f.limited = false
	search("exa-free")
	if f.hits["tavily"] != 2 {
		t.Fatalf("tavily was asked again while resting: %v", f.hits)
	}

	// DeepSeek gets at most 128 characters and gives its summary.
	if err := os.WriteFile(filepath.Join(dir, "deepseek_key"), []byte("d"), 0o600); err != nil {
		t.Fatal(err)
	}
	reply, err := s.Search(context.Background(), strings.Repeat("é", 200), 3)
	if err != nil || reply.Service != "deepseek" || reply.Results[0].Snippet != "Go is a language" {
		t.Fatalf("deepseek: %+v %v", reply, err)
	}
	f.status["deepseek"] = http.StatusPaymentRequired

	// When every service fails, the error names each.
	f.status["exa-free"], f.status["parallel"] = http.StatusTooManyRequests, http.StatusForbidden
	now = now.Add(2 * time.Hour)
	_, err = s.Search(context.Background(), "go", 3)
	if err == nil || !strings.Contains(err.Error(), "exa-free: rate limited") || !strings.Contains(err.Error(), "parallel: refused") {
		t.Fatalf("all failed: %v", err)
	}
}
