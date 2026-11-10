package models

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsady/den/internal/config"
)

func TestProtectMovesTheKeyAndProxies(t *testing.T) {
	var got *http.Request
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		if wait := r.URL.Query().Get("wait"); wait != "" {
			w.Header().Set("Retry-After", wait)
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte("data: hi\n\n"))
		if strings.Contains(r.URL.RawQuery, "usage") {
			w.Write([]byte(`data: {"choices":[],"usage":{"prompt_tokens":38211,"completion_tokens":5}}` + "\n\n"))
		}
	}))
	defer upstream.Close()

	root := t.TempDir()
	home := filepath.Join(root, "agent", ".fx")
	os.MkdirAll(filepath.Join(home, "provider-keys"), 0o700)
	os.MkdirAll(filepath.Join(home, "sessions", "v2"), 0o700)
	settings := `{"providers":{"go":{"protocol":"openai-chat-completions","base_url":"` + upstream.URL + `/openai/v1","auth":{"type":"bearer","env":"GO_KEY"},"models_dev":"go"},"local":{"protocol":"openai-chat-completions","base_url":"http://localhost:11434/v1","auth":{"type":"none"}}},"provider":"go"}`
	os.WriteFile(filepath.Join(home, "settings.json"), []byte(settings), 0o600)
	os.WriteFile(filepath.Join(home, "provider-keys", "go"), []byte("sk-real\n"), 0o600)

	cfg := config.Config{Secrets: filepath.Join(root, "secrets"), Sessions: filepath.Join(home, "sessions", "v2")}
	m := New(cfg)
	m.transport = upstream.Client().Transport
	m.Protect()

	if _, err := os.Stat(filepath.Join(home, "provider-keys", "go")); !os.IsNotExist(err) {
		t.Fatal("the key is still in the agent's profile")
	}
	data, _ := os.ReadFile(filepath.Join(home, "settings.json"))
	var after struct {
		Provider  string
		Providers map[string]struct {
			BaseURL   string `json:"base_url"`
			Auth      map[string]string
			ModelsDev string `json:"models_dev"`
		}
	}
	if err := json.Unmarshal(data, &after); err != nil {
		t.Fatal(err)
	}
	p := after.Providers["go"]
	if p.BaseURL != "http://"+Proxy+"/go" || p.Auth["type"] != "none" || p.ModelsDev != "go" || after.Provider != "go" {
		t.Fatalf("settings: %s", data)
	}
	if after.Providers["local"].BaseURL != "http://localhost:11434/v1" {
		t.Fatalf("changed the local provider: %s", data)
	}

	proxy := httptest.NewServer(m)
	defer proxy.Close()
	req, _ := http.NewRequest(http.MethodPost, proxy.URL+"/go/chat/completions?x=1", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer agent")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "data: hi\n\n" || got.URL.Path != "/openai/v1/chat/completions" || got.URL.RawQuery != "x=1" || got.Header.Get("Authorization") != "Bearer sk-real" {
		t.Fatalf("proxied %q to %s?%s with %q", body, got.URL.Path, got.URL.RawQuery, got.Header.Get("Authorization"))
	}

	// The proxy notes the prompt size the provider reports.
	start := time.Now()
	resp, err = http.Post(proxy.URL+"/go/chat/completions?usage=1", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()
	if peak := m.Peak(start); peak != 38211 {
		t.Fatalf("peak %d", peak)
	}
	if peak := m.Peak(time.Now().Add(time.Second)); peak != 0 {
		t.Fatalf("peak after %d", peak)
	}

	// A rate limit fx would wait out for hours ends the turn instead.
	for wait, want := range map[string]int{"30": http.StatusTooManyRequests, "85148": http.StatusPaymentRequired} {
		resp, err := http.Post(proxy.URL+"/go/chat/completions?wait="+wait, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("Retry-After %s: %d, want %d", wait, resp.StatusCode, want)
		}
	}

	// A second pass finds nothing to move.
	m.Protect()
	if resp, _ := http.Get(proxy.URL + "/nope/x"); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("unknown provider: %d", resp.StatusCode)
	}
}

func TestConversationIsStableAcrossTurns(t *testing.T) {
	first := conversation([]byte(`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`))
	later := conversation([]byte(`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"},{"role":"assistant","content":"yo"},{"role":"user","content":"more"}]}`))
	other := conversation([]byte(`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"bye"}]}`))
	if first != later || first == other || !strings.HasPrefix(first, "den-") {
		t.Fatalf("%s %s %s", first, later, other)
	}
}

func TestLogin(t *testing.T) {
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"opencode":{"api":"https://opencode.ai/inference/openai/v1","models":{}},"zen":{"api":"https://zen.example/v1","models":{"glm-5":{},"beam-free":{}}},"bare":{"models":{}}}`))
	}))
	defer catalog.Close()
	catalogURL = catalog.URL

	root := t.TempDir()
	home := filepath.Join(root, "agent", ".fx")
	os.MkdirAll(filepath.Join(home, "provider-keys"), 0o700)
	os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{"yolo_acknowledged":true,"providers":{"opencode-go":{"protocol":"openai-chat-completions","base_url":"http://127.0.0.1:4100/opencode-go","auth":{"type":"none"}}},"models":{"opencode-go":"x"},"provider":"opencode-go"}`), 0o600)
	os.WriteFile(filepath.Join(home, "provider-keys", "opencode"), []byte("old"), 0o600)
	cfg := config.Config{Secrets: filepath.Join(root, "secrets"), Sessions: filepath.Join(home, "sessions", "v2")}
	m := New(cfg)

	if err := m.Login("opencode", "", " sk-zen\n", "kimi-k2.6"); err != nil {
		t.Fatal(err)
	}
	p, err := m.load("opencode")
	if err != nil || p.BaseURL != "https://opencode.ai/inference/openai/v1" || p.Key != "sk-zen" {
		t.Fatalf("secret: %+v %v", p, err)
	}
	data, _ := os.ReadFile(filepath.Join(home, "settings.json"))
	for _, want := range []string{`"yolo_acknowledged":true`, `"opencode-go":"x"`, `"opencode":"kimi-k2.6"`, `"provider":"opencode"`, `"base_url":"http://127.0.0.1:4100/opencode"`, `"models_dev":"opencode"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("settings lack %s: %s", want, data)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "provider-keys", "opencode")); !os.IsNotExist(err) {
		t.Error("left fx's own copy of the key")
	}

	// A provider added without a model gets one of its own for fx, and the
	// default stays.
	if err := m.Login("zen", "", "k", ""); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(home, "settings.json"))
	if !strings.Contains(string(data), `"zen":"beam-free"`) || !strings.Contains(string(data), `"provider":"opencode"`) {
		t.Fatalf("settings after zen: %s", data)
	}

	if err := m.Login("bare", "", "k", ""); err == nil || !strings.Contains(err.Error(), "--base-url") {
		t.Fatalf("no address: %v", err)
	}
	if err := m.Login("plain", "http://example.com/v1", "k", ""); err == nil {
		t.Fatal("took an http address")
	}

	// Without settings, den writes the first.
	os.Remove(filepath.Join(home, "settings.json"))
	if err := m.Login("own", "https://api.example.com/v1", "k", "m1"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(home, "settings.json")); !strings.Contains(string(data), `"provider":"own"`) {
		t.Fatalf("new settings: %s", data)
	}
}

func TestCodexGetsTheRealToken(t *testing.T) {
	var got *http.Request
	chatgpt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Write([]byte(`{"usage":{"input_tokens":1234}}`))
	}))
	defer chatgpt.Close()

	root := t.TempDir()
	cfg := config.Config{Secrets: filepath.Join(root, "secrets"), Sessions: filepath.Join(root, "agent", ".fx", "sessions", "v2")}
	os.MkdirAll(cfg.Secrets, 0o700)
	os.WriteFile(filepath.Join(cfg.Secrets, "codex"), []byte(`{"access_token":"real","refresh_token":"r","expiry":"2100-01-01T00:00:00Z","account_id":"acct-1"}`), 0o600)
	m := New(cfg)
	m.chatgpt = chatgpt.URL + "/backend-api/codex"
	proxy := httptest.NewServer(m)
	defer proxy.Close()

	start := time.Now()
	req, _ := http.NewRequest(http.MethodPost, proxy.URL+"/codex/responses", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer den")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()
	if got.URL.Path != "/backend-api/codex/responses" || got.Header.Get("Authorization") != "Bearer real" || got.Header.Get("chatgpt-account-id") != "acct-1" {
		t.Fatalf("sent %s with %q, account %q", got.URL.Path, got.Header.Get("Authorization"), got.Header.Get("chatgpt-account-id"))
	}
	if m.Peak(start) != 1234 {
		t.Fatalf("peak %d", m.Peak(start))
	}

	// fx's refresh gets a stand-in that names the account.
	resp, _ = http.Post(proxy.URL+"/codex-auth/oauth/token", "application/json", strings.NewReader(`{}`))
	var body struct {
		Access string `json:"access_token"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	payload, _ := base64.RawURLEncoding.DecodeString(strings.Split(body.Access, ".")[1])
	if !strings.Contains(string(payload), `"chatgpt_account_id":"acct-1"`) {
		t.Fatalf("stand-in %s", payload)
	}
}

func TestDefaultKeepsAChosenModel(t *testing.T) {
	home := filepath.Join(t.TempDir(), "agent", ".fx")
	os.MkdirAll(home, 0o700)
	os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{"provider":"opencode-go","models":{"opencode-go":"x"}}`), 0o600)
	m := New(config.Config{Sessions: filepath.Join(home, "sessions", "v2")})

	asked := 0
	offered := func() ([]string, error) { asked++; return []string{"gpt-a", "gpt-b"}, nil }
	if err := m.Default("codex", offered); err != nil {
		t.Fatal(err)
	}
	m.Default("codex", offered)
	data, _ := os.ReadFile(filepath.Join(home, "settings.json"))
	if asked != 1 || !strings.Contains(string(data), `"codex":"gpt-a"`) || !strings.Contains(string(data), `"provider":"opencode-go"`) {
		t.Fatalf("asked %d, settings %s", asked, data)
	}
}

func TestProxyMeasuresCalls(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "fail") {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"message":"slow down"}}`))
			return
		}
		flush := w.(http.Flusher)
		w.Write([]byte(`data: {"choices":[{"delta":{"role":"assistant","content":""}}]}` + "\n\n"))
		flush.Flush()
		time.Sleep(150 * time.Millisecond)
		w.Write([]byte(`data: {"choices":[{"delta":{"content":"Hi"}}]}` + "\n\n"))
		flush.Flush()
		time.Sleep(250 * time.Millisecond)
		w.Write([]byte(`data: {"choices":[],"usage":{"prompt_tokens":900,"completion_tokens":42}}` + "\n\n"))
	}))
	defer upstream.Close()

	root := t.TempDir()
	cfg := config.Config{State: filepath.Join(root, "state"), Secrets: filepath.Join(root, "secrets"), Sessions: filepath.Join(root, "agent", ".fx", "sessions", "v2")}
	os.MkdirAll(cfg.State, 0o700)
	os.MkdirAll(cfg.Secrets, 0o700)
	os.WriteFile(filepath.Join(cfg.Secrets, "provider_go"), []byte(`{"base_url":"`+upstream.URL+`/v1","key":"k"}`), 0o600)
	m := New(cfg)
	m.transport = upstream.Client().Transport
	proxy := httptest.NewServer(m)
	defer proxy.Close()

	for _, q := range []string{"", "?fail=1"} {
		resp, err := http.Post(proxy.URL+"/go/chat/completions"+q, "application/json", strings.NewReader(`{"model":"flash","messages":[]}`))
		if err != nil {
			t.Fatal(err)
		}
		io.ReadAll(resp.Body)
		resp.Body.Close()
	}
	http.Get(proxy.URL + "/go/models")

	calls := m.Calls(time.Now().Add(-time.Minute))
	if len(calls) != 2 {
		t.Fatalf("calls %+v", calls)
	}
	ok, failed := calls[0], calls[1]
	if ok.Provider != "go" || ok.Model != "flash" || ok.Status != 200 || ok.In != 900 || ok.Out != 42 {
		t.Fatalf("call %+v", ok)
	}
	if ok.First < 140 || ok.Gap < 240 || ok.Total < ok.First+ok.Gap {
		t.Fatalf("timings %+v", ok)
	}
	if failed.Status != 429 || !strings.Contains(failed.Error, "slow down") || failed.First != 0 {
		t.Fatalf("failed %+v", failed)
	}

	// The calls outlive a restart.
	if again := New(cfg).Calls(time.Now().Add(-time.Minute)); len(again) != 2 || again[0].Out != 42 {
		t.Fatalf("after restart %+v", again)
	}
}
