// Package codex connects den to the user's ChatGPT subscription, which fx
// calls Codex, and keeps its tokens away from the agent.
//
// den signs in with OpenAI's public Codex client, PKCE, and a random state.
// OpenAI sends the browser back to localhost:1455 on the user's computer, where
// den catches the address and hands it to Deliver. den keeps the tokens in
// its secrets and refreshes them itself. fx, which the agent runs, gets a
// stand-in session that never expires, and its Codex requests reach
// ChatGPT through den's model proxy, which puts the real token in.
package codex

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/unsady/den/internal/config"
)

const (
	// File is where den keeps the account's tokens.
	File = "codex"

	// Wait is how long den waits for the user to finish signing in.
	Wait = 15 * time.Minute

	// Redirect is where OpenAI sends the browser after the sign-in; its
	// Codex client takes only this address.
	Redirect = "http://localhost:1455/auth/callback"

	clientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	scope    = "openid profile email offline_access api.connectors.read api.connectors.invoke"
	// authClaim holds the ChatGPT account in OpenAI's tokens.
	authClaim = "https://api.openai.com/auth"

	// authFile is fx's Codex session in the agent's profile.
	authFile = "chatgpt-auth.json"
)

var (
	authURL  = "https://auth.openai.com/oauth/authorize"
	tokenURL = "https://auth.openai.com/oauth/token"
)

// ErrNotSignin means a line is not a sign-in's address.
var ErrNotSignin = errors.New("not a sign-in address")

// ErrNotConnected means there is no account, or OpenAI revoked it.
var ErrNotConnected = errors.New("ChatGPT is not connected: the user connects it with `den connect codex` on their computer")

type token struct {
	Access  string    `json:"access_token"`
	Refresh string    `json:"refresh_token"`
	Expiry  time.Time `json:"expiry"`
	Account string    `json:"account_id"`
	Email   string    `json:"email,omitempty"`
}

type Codex struct {
	cfg  config.Config
	http *http.Client
	// home is the agent's fx profile, ~/.fx.
	home string

	mu      sync.Mutex
	pending map[string]chan url.Values
}

func New(cfg config.Config) *Codex {
	return &Codex{
		cfg:     cfg,
		http:    &http.Client{Timeout: 30 * time.Second},
		home:    filepath.Dir(filepath.Dir(cfg.Sessions)),
		pending: map[string]chan url.Values{},
	}
}

// Signin is one sign-in the user has yet to finish.
type Signin struct {
	// URL is where the user signs in.
	URL string

	state    string
	verifier string
}

// Begin starts a sign-in.
func (c *Codex) Begin() *Signin {
	s := &Signin{state: random(), verifier: random()}
	challenge := sha256.Sum256([]byte(s.verifier))
	q := url.Values{
		"response_type":              {"code"},
		"client_id":                  {clientID},
		"redirect_uri":               {Redirect},
		"scope":                      {scope},
		"code_challenge":             {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method":      {"S256"},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
		"state":                      {s.state},
		"originator":                 {"fx"},
	}
	s.URL = authURL + "?" + q.Encode()
	return s
}

// Finish waits for the user to sign in, keeps the tokens, gives fx its
// stand-in session, and returns the account's address.
func (c *Codex) Finish(ctx context.Context, s *Signin) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, Wait)
	defer cancel()

	code, err := c.wait(ctx, s.state)
	if err != nil {
		return "", err
	}
	t, err := c.request(ctx, "application/x-www-form-urlencoded", url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"code_verifier": {s.verifier},
		"redirect_uri":  {Redirect},
	}.Encode())
	if err != nil {
		return "", err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.save(t); err != nil {
		return "", err
	}
	if err := c.stand(t.Account); err != nil {
		return "", err
	}
	if t.Email == "" {
		return "your account", nil
	}
	return t.Email, nil
}

func (c *Codex) wait(ctx context.Context, state string) (string, error) {
	ch := make(chan url.Values, 1)
	c.mu.Lock()
	c.pending[state] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, state)
		c.mu.Unlock()
	}()

	select {
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", errors.New("the sign-in was not finished in time")
		}
		return "", ctx.Err()
	case q := <-ch:
		switch e := q.Get("error"); {
		case e == "access_denied":
			return "", errors.New("the sign-in was cancelled")
		case e != "":
			return "", fmt.Errorf("OpenAI answered %s", e)
		case q.Get("code") == "":
			return "", errors.New("the address has no code: paste the whole address")
		}
		return q.Get("code"), nil
	}
}

// Deliver hands a sign-in the address the browser came back to. It returns
// ErrNotSignin when text holds no such address.
func (c *Codex) Deliver(text string) error {
	link := ""
	for _, field := range strings.Fields(text) {
		if strings.HasPrefix(field, Redirect) {
			link = field
		}
	}
	if link == "" {
		return ErrNotSignin
	}
	u, err := url.Parse(link)
	if err != nil {
		return errors.New("this address is cut: paste the whole address")
	}
	q := u.Query()

	c.mu.Lock()
	ch, ok := c.pending[q.Get("state")]
	c.mu.Unlock()
	if !ok {
		return errors.New("this sign-in has expired: run den connect codex again")
	}
	select {
	case ch <- q:
	default:
	}
	return nil
}

// Token returns an access token and the ChatGPT account, refreshed when the
// token is about to expire.
func (c *Codex) Token(ctx context.Context) (string, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	t, err := c.load()
	if err != nil {
		return "", "", err
	}
	if time.Until(t.Expiry) > 5*time.Minute {
		return t.Access, t.Account, nil
	}

	body, _ := json.Marshal(map[string]string{"client_id": clientID, "grant_type": "refresh_token", "refresh_token": t.Refresh})
	fresh, err := c.request(ctx, "application/json", string(body))
	var refused *grantError
	if errors.As(err, &refused) && refused.status/100 == 4 {
		// The user signed out everywhere, or OpenAI expired the session.
		os.Remove(c.path())
		return "", "", ErrNotConnected
	}
	if err != nil {
		return "", "", err
	}
	if fresh.Refresh == "" {
		fresh.Refresh = t.Refresh
	}
	if fresh.Email == "" {
		fresh.Email = t.Email
	}
	return fresh.Access, fresh.Account, c.save(fresh)
}

// Connected reports whether an account is connected.
func (c *Codex) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.load()
	return err == nil
}

// Stand gives fx its stand-in session again, as after a new home folder.
func (c *Codex) Stand() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, err := c.load()
	if err != nil {
		return nil
	}
	return c.stand(t.Account)
}

// Disconnect forgets the account and fx's session.
func (c *Codex) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	os.Remove(filepath.Join(c.home, authFile))
	if err := os.Remove(c.path()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Never is when the stand-in expires, so fx never refreshes it.
var Never = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)

// Stand is the token fx sends in place of the account's, which den
// replaces. fx reads the account from a token's claims, so it is shaped as
// an unsigned JWT that names the account.
func Stand(account string) string {
	claims, _ := json.Marshal(map[string]any{
		"exp":     Never.Unix(),
		authClaim: map[string]string{"chatgpt_account_id": account},
	})
	return "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".den"
}

// stand writes fx's Codex session: the stand-in token and the real account.
func (c *Codex) stand(account string) error {
	data, _ := json.Marshal(map[string]any{
		"version":       1,
		"access_token":  Stand(account),
		"refresh_token": "den",
		"expires_at_ms": Never.UnixMilli(),
		"account_id":    account,
	})
	if err := os.MkdirAll(c.home, 0o700); err != nil {
		return err
	}
	path := filepath.Join(c.home, authFile)
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		return err
	}
	// fx reads only a session its own user owns.
	if info, err := os.Stat(c.home); err == nil {
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			os.Chown(path+".tmp", int(st.Uid), int(st.Gid))
		}
	}
	return os.Rename(path+".tmp", path)
}

type grantError struct {
	status int
	Code   string `json:"error"`
	Detail string `json:"error_description"`
}

func (e *grantError) Error() string {
	if e.Detail != "" {
		return "OpenAI: " + e.Detail
	}
	if e.Code != "" {
		return "OpenAI: " + e.Code
	}
	return fmt.Sprintf("OpenAI answered %d", e.status)
}

// request asks OpenAI's token endpoint for tokens.
func (c *Codex) request(ctx context.Context, contentType, body string) (token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(body))
	if err != nil {
		return token{}, err
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := c.http.Do(req)
	if err != nil {
		return token{}, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return token{}, err
	}
	if resp.StatusCode != http.StatusOK {
		e := &grantError{status: resp.StatusCode}
		json.Unmarshal(data, e)
		return token{}, e
	}

	var got struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		ID      string `json:"id_token"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		return token{}, err
	}
	claims, err := decode(got.Access)
	if err != nil {
		return token{}, err
	}
	t := token{Access: got.Access, Refresh: got.Refresh, Expiry: time.Unix(claims.Exp, 0), Account: claims.Auth.Account}
	if t.Account == "" {
		return token{}, errors.New("OpenAI's token names no ChatGPT account")
	}
	if id, err := decode(got.ID); err == nil {
		t.Email = id.Email
	}
	return t, nil
}

type claims struct {
	Exp   int64  `json:"exp"`
	Email string `json:"email"`
	Auth  struct {
		Account string `json:"chatgpt_account_id"`
	} `json:"https://api.openai.com/auth"`
}

// decode reads a JWT's claims; den got it from OpenAI over TLS, so it does
// not check the signature.
func decode(jwt string) (claims, error) {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return claims{}, errors.New("OpenAI sent a token den cannot read")
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims{}, errors.New("OpenAI sent a token den cannot read")
	}
	var c claims
	if err := json.Unmarshal(data, &c); err != nil {
		return claims{}, errors.New("OpenAI sent a token den cannot read")
	}
	return c, nil
}

func (c *Codex) path() string { return filepath.Join(c.cfg.Secrets, File) }

func (c *Codex) load() (token, error) {
	data, err := os.ReadFile(c.path())
	if err != nil {
		return token{}, ErrNotConnected
	}
	var t token
	if err := json.Unmarshal(data, &t); err != nil || t.Refresh == "" {
		return token{}, ErrNotConnected
	}
	return t, nil
}

func (c *Codex) save(t token) error {
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.cfg.Secrets, 0o700); err != nil {
		return err
	}
	path := c.path()
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func random() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
