// Package search queries web search services in turn.
//
// Three services answer without a key: the free endpoints of Exa, Keenable,
// and Parallel. A key the user adds, which den holds and the agent never
// sees, goes first, since it comes with higher limits. Each service is used
// while it works. One that is rate limited, rejects its key, or fails is
// skipped for a while, and the next one answers.
package search

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/unsady/den/internal/config"
)

// Result is one search hit.
type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// Reply is the hits and the service that found them.
type Reply struct {
	Service string   `json:"service"`
	Results []Result `json:"results"`
}

// service is one search API.
type service struct {
	// key is the secret's file in the secrets directory, empty for a
	// service that needs none.
	key     string
	request func(ctx context.Context, key, query string, n int) (*http.Request, error)
	parse   func(body []byte) ([]Result, error)
}

// Order is the services' names in the order den tries them: the ones with
// a key first, then the free ones. Exa's results tend to be the best.
var Order = []string{"exa", "deepseek", "tavily", "brave", "exa-free", "keenable", "parallel"}

// session is a random id Parallel's free tier uses for rate limits. It
// changes with each start of den and says nothing about the user.
var session = func() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}()

var services = map[string]service{
	"brave": {
		key: "brave_key",
		request: func(ctx context.Context, key, query string, n int) (*http.Request, error) {
			r, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoints["brave"], nil)
			if err != nil {
				return nil, err
			}
			q := r.URL.Query()
			q.Set("q", query)
			q.Set("count", fmt.Sprint(n))
			r.URL.RawQuery = q.Encode()
			r.Header.Set("Accept", "application/json")
			r.Header.Set("X-Subscription-Token", key)
			return r, nil
		},
		parse: func(body []byte) ([]Result, error) {
			var v struct {
				Web struct {
					Results []struct{ Title, URL, Description string }
				}
			}
			err := json.Unmarshal(body, &v)
			var out []Result
			for _, r := range v.Web.Results {
				out = append(out, Result{r.Title, r.URL, r.Description})
			}
			return out, err
		},
	},
	"tavily": {
		key: "tavily_key",
		request: func(ctx context.Context, key, query string, n int) (*http.Request, error) {
			return post(ctx, endpoints["tavily"], map[string]any{"query": query, "max_results": n},
				"Authorization", "Bearer "+key)
		},
		parse: func(body []byte) ([]Result, error) {
			var v struct {
				Results []struct{ Title, URL, Content string }
			}
			err := json.Unmarshal(body, &v)
			var out []Result
			for _, r := range v.Results {
				out = append(out, Result{r.Title, r.URL, r.Content})
			}
			return out, err
		},
	},
	"exa": {
		key: "exa_key",
		request: func(ctx context.Context, key, query string, n int) (*http.Request, error) {
			return post(ctx, endpoints["exa"], map[string]any{
				"query": query, "numResults": n,
				"contents": map[string]any{"text": map[string]any{"maxCharacters": 400}},
			}, "x-api-key", key)
		},
		parse: func(body []byte) ([]Result, error) {
			var v struct {
				Results []struct{ Title, URL, Text string }
			}
			err := json.Unmarshal(body, &v)
			var out []Result
			for _, r := range v.Results {
				out = append(out, Result{r.Title, r.URL, r.Text})
			}
			return out, err
		},
	},
}

func init() {
	services["deepseek"] = service{
		key: "deepseek_key",
		request: func(ctx context.Context, key, query string, n int) (*http.Request, error) {
			// DeepSeek takes at most 128 characters.
			if runes := []rune(query); len(runes) > 128 {
				query = string(runes[:128])
			}
			return post(ctx, endpoints["deepseek"], map[string]any{"query": query, "count": n},
				"Authorization", "Bearer "+key)
		},
		parse: func(body []byte) ([]Result, error) {
			var v struct {
				Results []struct{ Title, URL, Snippet, Summary string }
			}
			err := json.Unmarshal(body, &v)
			var out []Result
			for _, r := range v.Results {
				// The summary is written for a model and says more than the
				// snippet.
				text := r.Summary
				if text == "" {
					text = r.Snippet
				}
				out = append(out, Result{r.Title, r.URL, text})
			}
			return out, err
		},
	}
	services["exa-free"] = service{
		request: func(ctx context.Context, _, query string, n int) (*http.Request, error) {
			return mcpCall(ctx, endpoints["exa-free"], "web_search_exa", map[string]any{"query": query, "numResults": n})
		},
		parse: func(body []byte) ([]Result, error) {
			text, err := mcpText(body)
			if err != nil {
				return nil, err
			}
			return exaBlocks(text), nil
		},
	}
	services["parallel"] = service{
		request: func(ctx context.Context, _, query string, n int) (*http.Request, error) {
			return mcpCall(ctx, endpoints["parallel"], "web_search", map[string]any{
				"objective": query, "search_queries": []string{query}, "session_id": session,
			})
		},
		parse: func(body []byte) ([]Result, error) {
			text, err := mcpText(body)
			if err != nil {
				return nil, err
			}
			var v struct {
				Results []struct {
					Title, URL string
					Excerpts   []string
				}
			}
			err = json.Unmarshal([]byte(text), &v)
			var out []Result
			for _, r := range v.Results {
				out = append(out, Result{r.Title, r.URL, strings.Join(r.Excerpts, " ")})
			}
			return out, err
		},
	}
	services["keenable"] = service{
		request: func(ctx context.Context, _, query string, n int) (*http.Request, error) {
			return post(ctx, endpoints["keenable"], map[string]any{"query": query, "max_results": n},
				"X-Keenable-Title", "den")
		},
		parse: func(body []byte) ([]Result, error) {
			var v struct {
				Results []struct{ Title, URL, Snippet, Description string }
			}
			err := json.Unmarshal(body, &v)
			var out []Result
			for _, r := range v.Results {
				snippet := r.Snippet
				if snippet == "" {
					snippet = r.Description
				}
				out = append(out, Result{r.Title, r.URL, snippet})
			}
			return out, err
		},
	}
}

// endpoints are the services' URLs. Tests point them at a fake.
var endpoints = map[string]string{
	"exa":      "https://api.exa.ai/search",
	"deepseek": "https://api.deepseek.com/v1/tools/search",
	"tavily":   "https://api.tavily.com/search",
	"brave":    "https://api.search.brave.com/res/v1/web/search",
	"exa-free": "https://mcp.exa.ai/mcp",
	"keenable": "https://api.keenable.ai/v1/search/public",
	"parallel": "https://search.parallel.ai/mcp",
}

// mcpCall builds an MCP tools/call request, the way Exa's and Parallel's
// free endpoints take a search.
func mcpCall(ctx context.Context, url, tool string, args map[string]any) (*http.Request, error) {
	r, err := post(ctx, url, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	}, "Accept", "application/json, text/event-stream")
	if err != nil {
		return nil, err
	}
	r.Header.Set("User-Agent", "den")
	return r, nil
}

// mcpText returns the text of an MCP tools/call answer, sent as JSON or as
// server-sent events, or the tool's error, such as a rate limit.
func mcpText(body []byte) (string, error) {
	candidates := []string{string(body)}
	for _, line := range strings.Split(string(body), "\n") {
		if data, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "data: "); ok {
			candidates = append(candidates, data)
		}
	}
	for _, c := range candidates {
		var v struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if json.Unmarshal([]byte(c), &v) != nil {
			continue
		}
		if v.Error != nil {
			return "", errors.New(v.Error.Message)
		}
		var texts []string
		for _, content := range v.Result.Content {
			texts = append(texts, content.Text)
		}
		text := strings.Join(texts, "\n")
		if v.Result.IsError {
			return "", errors.New(text)
		}
		return text, nil
	}
	return "", errors.New("not an MCP answer")
}

// exaBlocks reads Exa's free answer: blocks of Title:, URL:, and
// Highlights: lines, split by lines of ---.
func exaBlocks(text string) []Result {
	var out []Result
	for _, block := range strings.Split(text, "\n---\n") {
		var r Result
		var highlights []string
		inHighlights := false
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(line, "Title:"):
				r.Title, inHighlights = strings.TrimSpace(strings.TrimPrefix(line, "Title:")), false
			case strings.HasPrefix(line, "URL:"):
				r.URL, inHighlights = strings.TrimSpace(strings.TrimPrefix(line, "URL:")), false
			case strings.HasPrefix(line, "Highlights:"):
				inHighlights = true
			case strings.HasPrefix(line, "Published:"), strings.HasPrefix(line, "Author:"):
				inHighlights = false
			case inHighlights && line != "":
				highlights = append(highlights, line)
			}
		}
		if r.URL != "" {
			r.Snippet = strings.Join(highlights, " ")
			out = append(out, r)
		}
	}
	return out
}

func post(ctx context.Context, url string, body any, header, value string) (*http.Request, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(header, value)
	return r, nil
}

type Searcher struct {
	cfg    config.Config
	client *http.Client
	now    func() time.Time

	mu sync.Mutex
	// resting maps a service to when den may try it again.
	resting map[string]time.Time
}

func New(cfg config.Config) *Searcher {
	return &Searcher{
		cfg:     cfg,
		client:  &http.Client{Timeout: 30 * time.Second},
		now:     time.Now,
		resting: map[string]time.Time{},
	}
}

// Search asks the services in order for n results and returns the first
// answer. A service that fails rests: an hour after a rate limit or an
// error, a day after it refuses den or runs out of quota.
func (s *Searcher) Search(ctx context.Context, query string, n int) (Reply, error) {
	var tried []string
	for _, name := range Order {
		svc := services[name]
		var key string
		if svc.key != "" {
			data, err := os.ReadFile(filepath.Join(s.cfg.Secrets, svc.key))
			key = strings.TrimSpace(string(data))
			if err != nil || key == "" {
				continue
			}
		}
		if s.rests(name) {
			continue
		}

		results, rest, err := s.ask(ctx, svc, key, query, n)
		if err == nil {
			if len(results) > n {
				results = results[:n]
			}
			for i := range results {
				results[i].Snippet = clip(results[i].Snippet, 500)
			}
			return Reply{Service: name, Results: results}, nil
		}
		if ctx.Err() != nil {
			return Reply{}, ctx.Err()
		}
		log.Printf("search: %s: %v; resting %s", name, err, rest)
		s.rest(name, rest)
		tried = append(tried, name+": "+err.Error())
	}
	if len(tried) == 0 {
		return Reply{}, errors.New("every search service is resting after recent failures; try again in an hour")
	}
	return Reply{}, fmt.Errorf("every search service failed: %s", strings.Join(tried, "; "))
}

func (s *Searcher) ask(ctx context.Context, svc service, key, query string, n int) ([]Result, time.Duration, error) {
	r, err := svc.request(ctx, key, query, n)
	if err != nil {
		return nil, time.Hour, err
	}
	resp, err := s.client.Do(r)
	if err != nil {
		return nil, time.Hour, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, time.Hour, err
	}

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, time.Hour, fmt.Errorf("rate limited")
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusPaymentRequired,
		resp.StatusCode == http.StatusForbidden, resp.StatusCode == 432:
		return nil, 24 * time.Hour, fmt.Errorf("refused, out of quota, or the key is wrong (HTTP %d)", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return nil, time.Hour, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	results, err := svc.parse(body)
	if err != nil {
		return nil, time.Hour, fmt.Errorf("unreadable answer: %s", clip(err.Error(), 200))
	}
	return results, 0, nil
}

// clip collapses whitespace in text and cuts it to n runes.
func clip(text string, n int) string {
	text = strings.Join(strings.Fields(text), " ")
	if runes := []rune(text); len(runes) > n {
		return string(runes[:n-1]) + "…"
	}
	return text
}

func (s *Searcher) rests(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now().Before(s.resting[name])
}

func (s *Searcher) rest(name string, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resting[name] = s.now().Add(d)
}
