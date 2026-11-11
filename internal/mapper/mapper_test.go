package mapper

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// provider answers like an OpenAI-style API: the item's text decides the
// reply.
func provider(t *testing.T, calls *atomic.Int32) *httptest.Server {
	var once sync.Once
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role, Content string
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "cheap" || len(req.Messages) != 2 || !strings.HasPrefix(req.Messages[0].Content, "Classify.") {
			t.Errorf("request: %+v", req)
		}
		if !strings.HasPrefix(r.Header.Get("X-Opencode-Session"), "den-map-") {
			t.Errorf("session header %q", r.Header.Get("X-Opencode-Session"))
		}

		item := req.Messages[1].Content
		content := `{"kind":"fruit","reason":"It is sweet and grows on a tree in an orchard."}`
		switch {
		case strings.Contains(item, "flaky"):
			failed := false
			once.Do(func() { failed = true })
			if failed {
				http.Error(w, "busy", http.StatusServiceUnavailable)
				return
			}
		case strings.Contains(item, "fenced"):
			content = "Here it is:\n```json\n{\"kind\":\"veg\"}\n```"
		case strings.Contains(item, "prose"):
			content = "I cannot tell."
		case strings.Contains(item, "refused"):
			http.Error(w, "no such model", http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
			"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 5, "prompt_tokens_details": map[string]any{"cached_tokens": 80}, "opencode": map[string]any{"list_cost": 0.001}},
		})
	}))
}

func results(t *testing.T, path string) map[int]Result {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int]Result{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		var r Result
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		out[r.Line] = r
	}
	return out
}

func TestRun(t *testing.T) {
	var calls atomic.Int32
	srv := provider(t, &calls)
	defer srv.Close()

	items := strings.Join([]string{
		`{"id": 7, "text": "apple"}`,
		`"fenced carrot"`,
		``,
		`{"id": "x", "text": "flaky pear"}`,
		`{"text": "prose"}`,
		`{"text": "refused"}`,
	}, "\n")
	out := filepath.Join(t.TempDir(), "items.out.jsonl")
	opts := Options{URL: srv.URL, Model: "cheap", Instructions: "Classify.", Workers: 3}

	sum, err := Run(context.Background(), opts, strings.NewReader(items), out)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Items != 5 || sum.Answered != 3 || sum.Failed != 2 {
		t.Errorf("summary %+v", sum)
	}
	if sum.Values["kind"]["fruit"] != 2 || sum.Values["kind"]["veg"] != 1 || sum.Values["reason"] != nil {
		t.Errorf("values %v", sum.Values)
	}

	got := results(t, out)
	if string(got[1].ID) != "7" || !strings.Contains(string(got[1].Answer), `"kind":"fruit"`) || got[1].Cached != 80 || got[1].Cost != 0.001 {
		t.Errorf("line 1: %+v", got[1])
	}
	if string(got[2].Answer) != `{"kind":"veg"}` {
		t.Errorf("fenced reply: %+v", got[2])
	}
	if string(got[4].ID) != `"x"` || got[4].Error != "" {
		t.Errorf("retried request: %+v", got[4])
	}
	if got[5].Error != "the reply is not JSON" || got[5].Text != "I cannot tell." {
		t.Errorf("prose reply: %+v", got[5])
	}
	if !strings.Contains(got[6].Error, "HTTP 400") {
		t.Errorf("refused request: %+v", got[6])
	}
	// The prose reply is asked three times, the refusal once.
	if n := calls.Load(); n != 1+1+2+3+1 {
		t.Errorf("%d calls", n)
	}

	// Resuming asks again only about the items that failed.
	calls.Store(0)
	sum, err = Run(context.Background(), Options{URL: srv.URL, Model: "cheap", Instructions: "Classify.", Resume: true}, strings.NewReader(items), out)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Kept != 3 || sum.Failed != 2 || calls.Load() != 4 {
		t.Errorf("resume: %+v after %d calls", sum, calls.Load())
	}
	lines := results(t, out)
	keys := make([]int, 0, len(lines))
	for k := range lines {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []int{1, 2, 4, 5, 6}) {
		t.Errorf("resumed output has lines %v", keys)
	}
}

func TestClaudeInstructionsAreCached(t *testing.T) {
	var system json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		system = req.Messages[0].Content
		w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
	}))
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "out.jsonl")
	text := "Classify." + reply
	marked, _ := json.Marshal([]map[string]any{{"type": "text", "text": text, "cache_control": map[string]string{"type": "ephemeral"}}})
	plain, _ := json.Marshal(text)
	for model, want := range map[string][]byte{"claude-haiku-5-5": marked, "deepseek": plain} {
		if _, err := Run(context.Background(), Options{URL: srv.URL, Model: model, Instructions: "Classify."}, strings.NewReader(`"x"`), out); err != nil {
			t.Fatal(err)
		}
		var got, wanted any
		json.Unmarshal(system, &got)
		json.Unmarshal(want, &wanted)
		if fmt.Sprint(got) != fmt.Sprint(wanted) {
			t.Errorf("%s: system %s", model, system)
		}
	}
}

func TestProgress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(30 * time.Millisecond)
		w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
	}))
	defer srv.Close()

	var buf syncBuffer
	opts := Options{URL: srv.URL, Model: "m", Instructions: "x", Workers: 1, Progress: &buf, Every: 20 * time.Millisecond}
	if _, err := Run(context.Background(), opts, strings.NewReader("1\n2\n3\n4\n"), filepath.Join(t.TempDir(), "out.jsonl")); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, "of 4 done, 0 failed") {
		t.Errorf("progress %q", got)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestExtract(t *testing.T) {
	for in, want := range map[string]string{
		`{"a":1}`:                       `{"a":1}`,
		"```json\n[1,2]\n```":           `[1,2]`,
		`Sure: {"a":{"b":2}} hope this`: `{"a":{"b":2}}`,
		`"just a string"`:               `"just a string"`,
		`no json here`:                  ``,
	} {
		if got := string(extract(in)); got != want {
			t.Errorf("extract(%q) = %q, want %q", in, got, want)
		}
	}
}
