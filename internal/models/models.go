// Package models keeps the model provider's API key from the agent.
//
// fx login saves a provider's key in the agent's own profile, where the
// agent could read it. den moves each such key into its secrets, together
// with the provider's address, and points fx at den's proxy on the
// loopback address instead. The proxy adds the key to each request and
// sends it only to the address saved with that key, so the agent can use
// the model but cannot carry the key elsewhere.
package models

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/unsady/den/internal/codex"
	"github.com/unsady/den/internal/config"
)

// Proxy is where den serves the providers to fx.
const Proxy = "127.0.0.1:4100"

// provider is what den keeps for one provider, in the secret
// provider_<name>.
type provider struct {
	BaseURL string `json:"base_url"`
	Key     string `json:"key"`
}

type Models struct {
	cfg config.Config
	// home is the agent's fx profile, ~/.fx.
	home string
	// transport reaches the providers; nil is Go's default.
	transport http.RoundTripper
	// codex holds the ChatGPT account, when den has one.
	codex *codex.Codex
	// chatgpt is ChatGPT's Codex API; tests change it.
	chatgpt string

	mu sync.Mutex
	// prompts are the prompt sizes of recent requests, oldest first.
	prompts []prompt
	// calls are the recent model requests, oldest first, and written how
	// many lines the calls file holds.
	calls   []Call
	written int
}

// prompt is the size a provider reported for one request's prompt.
type prompt struct {
	at     time.Time
	tokens int
}

// Peak returns the largest prompt of the requests since a time, in tokens:
// within one turn the context only grows, so it is the turn's context size.
// It is 0 when no provider reported one.
func (m *Models) Peak(since time.Time) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	peak := 0
	for _, p := range m.prompts {
		if !p.at.Before(since) {
			peak = max(peak, p.tokens)
		}
	}
	return peak
}

func (m *Models) record(tokens int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prompts = append(m.prompts, prompt{at: time.Now(), tokens: tokens})
	if len(m.prompts) > 500 {
		m.prompts = m.prompts[len(m.prompts)-500:]
	}
}

// promptTokens finds the prompt size in Chat Completions' usage and in the
// Responses API's, which ChatGPT's Codex speaks.
var promptTokens = regexp.MustCompile(`"(?:prompt|input)_tokens"\s*:\s*(\d+)`)

func New(cfg config.Config) *Models {
	// The sessions live at ~/.fx/sessions/v2.
	m := &Models{cfg: cfg, home: filepath.Dir(filepath.Dir(cfg.Sessions)), codex: codex.New(cfg), chatgpt: "https://chatgpt.com/backend-api/codex"}
	m.loadCalls()
	return m
}

// Serve moves new keys now and then, and proxies the providers until the
// listener fails.
func (m *Models) Serve(stop <-chan struct{}) error {
	l, err := net.Listen("tcp", Proxy)
	if err != nil {
		return err
	}
	go func() {
		<-stop
		l.Close()
	}()

	go func() {
		tick := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		for {
			m.Protect()
			select {
			case <-stop:
				return
			case <-tick.C:
			}
		}
	}()

	server := &http.Server{Handler: m, ReadHeaderTimeout: 30 * time.Second}
	if err := server.Serve(l); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

// ServeHTTP sends /<name>/<path> to the provider saved as name.
func (m *Models) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	switch name {
	case "codex":
		m.serveCodex(w, r, rest)
		return
	case "codex-auth":
		m.standToken(w)
		return
	}
	p, err := m.load(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	base, err := url.Parse(p.BaseURL)
	if err != nil || base.Scheme != "https" {
		http.Error(w, "the provider's saved address is not https", http.StatusBadGateway)
		return
	}
	// The path only extends the provider's own: the key goes to its host
	// alone.
	target := base.JoinPath(rest)
	target.RawQuery = r.URL.RawQuery

	start := time.Now()
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	// fx names the conversation to OpenCode only when it calls OpenCode
	// itself, and OpenCode refuses a request without one.
	session := ""
	if strings.EqualFold(base.Hostname(), "opencode.ai") && r.Header.Get("X-Opencode-Session") == "" {
		session = conversation(body)
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(out *httputil.ProxyRequest) {
			out.Out.URL = target
			out.Out.Host = target.Host
			out.Out.Header.Del("Cookie")
			out.Out.Header.Set("Authorization", "Bearer "+p.Key)
			if session != "" {
				out.Out.Header.Set("X-Opencode-Session", session)
			}
		},
		// Answers stream, so each piece goes on at once.
		FlushInterval: -1,
		Transport:     m.transport,
	}
	m.watch(proxy, rest, name, requestModel(body), start)
	proxy.ServeHTTP(w, r)
}

// readBody reads a request's body and puts it back for the provider.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return nil, false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	return body, true
}

// watch makes the proxy turn rate limits that last hours into refusals,
// and measure each request for an answer, failed ones included.
func (m *Models) watch(proxy *httputil.ReverseProxy, path, provider, model string, start time.Time) {
	proxy.ModifyResponse = func(resp *http.Response) error {
		refuseLongWait(resp)
		if generation(path) {
			resp.Body = m.measure(resp.Body, start, provider, model, resp.StatusCode)
		}
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if generation(path) {
			m.addCall(Call{At: start, Provider: provider, Model: model, Status: http.StatusBadGateway, Total: int(time.Since(start).Milliseconds()), Error: err.Error()})
		}
		log.Printf("models: %s: %v", provider, err)
		w.WriteHeader(http.StatusBadGateway)
	}
}

// conversation names a chat request's conversation by its opening
// messages, which stay the same on every turn, so the provider can route
// all of its turns alike and reuse its cache.
func conversation(body []byte) string {
	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	json.Unmarshal(body, &req)
	h := sha256.New()
	for _, m := range req.Messages[:min(2, len(req.Messages))] {
		h.Write(m)
	}
	return "den-" + hex.EncodeToString(h.Sum(nil))[:32]
}

// names are the provider ids fx allows.
var names = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func (m *Models) load(name string) (provider, error) {
	var p provider
	if !names.MatchString(name) {
		return p, fmt.Errorf("no provider %q", name)
	}
	data, err := os.ReadFile(filepath.Join(m.cfg.Secrets, "provider_"+name))
	if err != nil || json.Unmarshal(data, &p) != nil || p.Key == "" {
		return p, fmt.Errorf("den has no key for the provider %q: sign in with fx login", name)
	}
	return p, nil
}

// Protect moves each key that fx login saved in the agent's profile into
// den's secrets and points that provider at den's proxy.
func (m *Models) Protect() {
	keys, err := os.ReadDir(filepath.Join(m.home, "provider-keys"))
	if err != nil || len(keys) == 0 {
		return
	}

	settingsPath := filepath.Join(m.home, "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		return
	}
	var providers map[string]map[string]json.RawMessage
	if err := json.Unmarshal(settings["providers"], &providers); err != nil {
		return
	}

	changed := false
	for _, entry := range keys {
		name := entry.Name()
		keyPath := filepath.Join(m.home, "provider-keys", name)
		def, ok := providers[name]
		if !ok {
			continue
		}
		var base string
		json.Unmarshal(def["base_url"], &base)
		u, err := url.Parse(base)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			// A key for a local server, or one already behind the proxy.
			continue
		}
		key, err := os.ReadFile(keyPath)
		if err != nil || strings.TrimSpace(string(key)) == "" {
			continue
		}

		secret, _ := json.Marshal(provider{BaseURL: base, Key: strings.TrimSpace(string(key))})
		if err := writeSecret(filepath.Join(m.cfg.Secrets, "provider_"+name), secret); err != nil {
			log.Printf("models: %s: %v", name, err)
			continue
		}
		def["base_url"], _ = json.Marshal("http://" + Proxy + "/" + name)
		def["auth"] = json.RawMessage(`{"type":"none"}`)
		providers[name] = def
		os.Remove(keyPath)
		changed = true
		log.Printf("models: moved the %s key out of the agent's profile", name)
	}
	if !changed {
		return
	}

	settings["providers"], _ = json.Marshal(providers)
	out, err := json.Marshal(settings)
	if err != nil {
		return
	}
	if err := replace(settingsPath, out); err != nil {
		log.Printf("models: %v", err)
	}
}

func writeSecret(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// replace rewrites a file of the agent's, keeping its owner and mode.
func replace(path string, data []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	tmp := path + ".den"
	if err := os.WriteFile(tmp, append(bytes.TrimSpace(data), '\n'), info.Mode().Perm()); err != nil {
		return err
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		os.Chown(tmp, int(st.Uid), int(st.Gid))
	}
	return os.Rename(tmp, path)
}

// catalogURL lists the providers den can sign in to.
var catalogURL = "https://models.dev/api.json"

// Login keeps a provider's key in den's secrets and points the agent's fx
// at den's proxy for it, with model as the default. The provider's address
// comes from baseURL, or from models.dev when it is empty. The key never
// enters the agent's profile.
func (m *Models) Login(id, baseURL, key, model string) error {
	if !names.MatchString(id) {
		return fmt.Errorf("%q is not a provider id", id)
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("the key is empty")
	}

	var offered []string
	if baseURL == "" {
		var err error
		if baseURL, offered, err = catalogProvider(id); err != nil {
			return err
		}
	}
	if u, err := url.Parse(baseURL); err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%q is not an https address", baseURL)
	}

	secret, _ := json.Marshal(provider{BaseURL: baseURL, Key: key})
	if err := writeSecret(filepath.Join(m.cfg.Secrets, "provider_"+id), secret); err != nil {
		return err
	}

	settingsPath := filepath.Join(m.home, "settings.json")
	settings := map[string]json.RawMessage{}
	if data, err := os.ReadFile(settingsPath); err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("%s: %v", settingsPath, err)
		}
	}
	providers := map[string]json.RawMessage{}
	json.Unmarshal(settings["providers"], &providers)
	providers[id], _ = json.Marshal(map[string]any{
		"protocol":   "openai-chat-completions",
		"base_url":   "http://" + Proxy + "/" + id,
		"auth":       map[string]string{"type": "none"},
		"models_dev": id,
	})
	settings["providers"], _ = json.Marshal(providers)
	models := map[string]string{}
	json.Unmarshal(settings["models"], &models)
	switch {
	case model != "":
		models[id] = model
		settings["provider"], _ = json.Marshal(id)
	case models[id] == "" && len(offered) > 0:
		// fx takes no provider without a model of its own. The chat
		// passes the model it chose, so any of the provider's will do.
		models[id] = offered[0]
	}
	settings["models"], _ = json.Marshal(models)
	// A key fx login saved earlier is no longer needed.
	os.Remove(filepath.Join(m.home, "provider-keys", id))

	out, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	if _, err := os.Stat(settingsPath); err != nil {
		return m.create(settingsPath, out)
	}
	return replace(settingsPath, out)
}

// create writes a new file in the agent's profile, owned like the profile.
func (m *Models) create(path string, data []byte) error {
	if err := os.MkdirAll(m.home, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return err
	}
	// The profile's parent is the agent's home.
	if info, err := os.Stat(filepath.Dir(m.home)); err == nil {
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			os.Chown(m.home, int(st.Uid), int(st.Gid))
			os.Chown(path, int(st.Uid), int(st.Gid))
		}
	}
	return nil
}

// longWait is the longest Retry-After den lets fx wait out.
const longWait = 10 * time.Minute

// refuseLongWait turns a rate limit that lasts longer than longWait, such as
// a used-up daily allowance, into a refusal. fx retries a rate limit, and
// would keep the turn waiting for hours; a refusal ends the turn with the
// provider's message.
func refuseLongWait(resp *http.Response) {
	if resp.StatusCode != http.StatusTooManyRequests {
		return
	}
	wait, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	if err != nil || time.Duration(wait)*time.Second <= longWait {
		return
	}
	resp.StatusCode = http.StatusPaymentRequired
	resp.Status = "402 Payment Required"
	resp.Header.Del("Retry-After")
}

// catalogProvider finds a provider's address and models on models.dev.
func catalogProvider(id string) (string, []string, error) {
	client := &http.Client{Timeout: time.Minute}
	resp, err := client.Get(catalogURL)
	if err != nil {
		return "", nil, fmt.Errorf("models.dev: %v", err)
	}
	defer resp.Body.Close()

	var catalog map[string]struct {
		API    string                     `json:"api"`
		Models map[string]json.RawMessage `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&catalog); err != nil {
		return "", nil, fmt.Errorf("models.dev: %v", err)
	}
	p, ok := catalog[id]
	if !ok {
		return "", nil, fmt.Errorf("models.dev has no provider %q", id)
	}
	if p.API == "" {
		return "", nil, fmt.Errorf("models.dev gives no address for %q: pass --base-url", id)
	}
	models := slices.Sorted(maps.Keys(p.Models))
	return p.API, models, nil
}

// serveCodex passes fx's Codex requests on to ChatGPT with the account's
// token in place of the stand-in fx sends.
func (m *Models) serveCodex(w http.ResponseWriter, r *http.Request, rest string) {
	access, account, err := m.codex.Token(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	target, _ := url.Parse(m.chatgpt + "/" + rest)
	target.RawQuery = r.URL.RawQuery
	start := time.Now()
	body, ok := readBody(w, r)
	if !ok {
		return
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(out *httputil.ProxyRequest) {
			out.Out.URL = target
			out.Out.Host = target.Host
			out.Out.Header.Del("Cookie")
			out.Out.Header.Set("Authorization", "Bearer "+access)
			out.Out.Header.Set("chatgpt-account-id", account)
		},
		FlushInterval: -1,
		Transport:     m.transport,
	}
	m.watch(proxy, rest, "codex", requestModel(body), start)
	if rest == "models" {
		watched := proxy.ModifyResponse
		proxy.ModifyResponse = func(resp *http.Response) error {
			m.keepCodexEfforts(resp)
			return watched(resp)
		}
	}
	proxy.ServeHTTP(w, r)
}

// standToken answers fx's token refresh with another stand-in: den keeps
// the real tokens fresh itself.
func (m *Models) standToken(w http.ResponseWriter) {
	account := ""
	if _, a, err := m.codex.Token(context.Background()); err == nil {
		account = a
	}
	jwt := codex.Stand(account)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"access_token": jwt, "refresh_token": "den", "id_token": jwt, "expires_in": int(time.Until(codex.Never).Seconds())})
}

// Default gives provider a model of its own in fx's settings when it has
// none, since fx refuses to use or even list such a provider. The chat's
// /model choice still overrides it.
func (m *Models) Default(provider string, offered func() ([]string, error)) error {
	path := filepath.Join(m.home, "settings.json")
	settings := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("%s: %v", path, err)
		}
	}
	models := map[string]string{}
	json.Unmarshal(settings["models"], &models)
	if models[provider] != "" {
		return nil
	}

	list, err := offered()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return fmt.Errorf("%s offers no models", provider)
	}
	models[provider] = list[0]
	settings["models"], _ = json.Marshal(models)
	out, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	if data == nil {
		return m.create(path, out)
	}
	return replace(path, out)
}
