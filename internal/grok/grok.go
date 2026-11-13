// Package grok connects den to the user's Grok subscription and keeps its
// tokens away from the agent.
//
// den signs in with xAI's public Grok CLI client, the one fx uses, with
// PKCE and a random state. xAI sends the browser back to a loopback address
// on the user's computer, where den catches it and hands it to Deliver. den
// keeps the tokens in its secrets and refreshes them itself. fx, which the
// agent runs, gets a stand-in session that never expires, and its Grok
// requests reach xAI through den's model proxy, which puts the real token
// in.
package grok

import (
	"context"
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
	"time"

	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/proc"
	"github.com/unsady/den/internal/signin"
)

const (
	// File is where den keeps the account's tokens.
	File = "grok"

	// Wait is how long den waits for the user to finish signing in.
	Wait = 15 * time.Minute

	// Redirect is where xAI sends the browser after the sign-in. Its client
	// takes any loopback port.
	Redirect = "http://127.0.0.1:56121/callback"

	clientID = "b1a00492-073a-47ea-816f-4c329264a828"
	scope    = "openid profile email offline_access grok-cli:access api:access"

	// authFile is fx's Grok session in the agent's profile.
	authFile = "grok-auth.json"
)

var (
	authURL     = "https://auth.x.ai/oauth2/authorize"
	tokenURL    = "https://auth.x.ai/oauth2/token"
	userinfoURL = "https://auth.x.ai/oauth2/userinfo"
)

// ErrNotSignin means a line is not a sign-in's address.
var ErrNotSignin = signin.ErrNotSignin

// ErrNotConnected means there is no account, or xAI revoked it.
var ErrNotConnected = errors.New("Grok is not connected: the user connects it with `den connect grok` on their computer")

type token struct {
	Access  string    `json:"access_token"`
	Refresh string    `json:"refresh_token"`
	Expiry  time.Time `json:"expiry"`
	Account string    `json:"account_id"`
	Email   string    `json:"email,omitempty"`
}

type Grok struct {
	cfg  config.Config
	http *http.Client
	// home is the agent's fx profile, ~/.fx.
	home string

	mu      sync.Mutex
	pending *signin.Pending
}

func New(cfg config.Config) *Grok {
	return &Grok{
		cfg:     cfg,
		http:    &http.Client{Timeout: 30 * time.Second},
		home:    filepath.Dir(filepath.Dir(cfg.Sessions)),
		pending: &signin.Pending{Who: "xAI", Redirect: Redirect, Again: "den connect grok"},
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
func (g *Grok) Begin() *Signin {
	s := &Signin{state: signin.Random(), verifier: signin.Random()}
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {Redirect},
		"scope":                 {scope},
		"code_challenge":        {signin.Challenge(s.verifier)},
		"code_challenge_method": {"S256"},
		"state":                 {s.state},
		"referrer":              {"fx"},
	}
	s.URL = authURL + "?" + q.Encode()
	return s
}

// Finish waits for the user to sign in, keeps the tokens, gives fx its
// stand-in session, and returns the account's address.
func (g *Grok) Finish(ctx context.Context, s *Signin) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, Wait)
	defer cancel()

	code, err := g.pending.Wait(ctx, s.state)
	if err != nil {
		return "", err
	}
	t, err := g.request(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"code_verifier": {s.verifier},
		"redirect_uri":  {Redirect},
	})
	if err != nil {
		return "", err
	}
	if t.Refresh == "" {
		return "", errors.New("xAI sent no refresh token")
	}
	if t.Account, t.Email, err = g.user(ctx, t.Access); err != nil {
		return "", err
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.save(t); err != nil {
		return "", err
	}
	if err := g.stand(t.Account); err != nil {
		return "", err
	}
	if t.Email == "" {
		return "your account", nil
	}
	return t.Email, nil
}

// Deliver hands a sign-in the address the browser came back to. It returns
// ErrNotSignin when text holds no such address.
func (g *Grok) Deliver(text string) error {
	return g.pending.Deliver(text)
}

// Token returns an access token, refreshed when it is about to expire.
func (g *Grok) Token(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	t, err := g.load()
	if err != nil {
		return "", err
	}
	if time.Until(t.Expiry) > 5*time.Minute {
		return t.Access, nil
	}

	fresh, err := g.request(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {t.Refresh},
	})
	var refused *grantError
	if errors.As(err, &refused) && refused.status/100 == 4 {
		// The user signed out everywhere, or xAI expired the session.
		os.Remove(g.path())
		return "", ErrNotConnected
	}
	if err != nil {
		return "", err
	}
	t.Access, t.Expiry = fresh.Access, fresh.Expiry
	if fresh.Refresh != "" {
		t.Refresh = fresh.Refresh
	}
	return t.Access, g.save(t)
}

// Account returns the connected account's id, which fx sends with each
// request, or "" when none is connected.
func (g *Grok) Account() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	t, err := g.load()
	if err != nil {
		return ""
	}
	return t.Account
}

// Connected reports whether an account is connected.
func (g *Grok) Connected() bool {
	return g.Account() != ""
}

// Stand gives fx its stand-in session again, as after a new home folder.
func (g *Grok) Stand() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	t, err := g.load()
	if err != nil {
		return nil
	}
	return g.stand(t.Account)
}

// Disconnect forgets the account and fx's session.
func (g *Grok) Disconnect() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	os.Remove(filepath.Join(g.home, authFile))
	if err := os.Remove(g.path()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Never is when the stand-in expires, so fx never refreshes it.
var Never = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)

// Stand is the token fx sends in place of the account's, which den
// replaces.
const Stand = "den"

// stand writes fx's Grok session: the stand-in token and the real account.
func (g *Grok) stand(account string) error {
	data, _ := json.Marshal(map[string]any{
		"version":       1,
		"access_token":  Stand,
		"refresh_token": Stand,
		"expires_at_ms": Never.UnixMilli(),
		"account_id":    account,
	})
	// fx reads only a session its own user owns, and the profile is the
	// agent's, so den writes it as the agent.
	return proc.WriteFile(g.cfg, filepath.Join(g.home, authFile), data, 0o600)
}

type grantError struct {
	status int
	Code   string `json:"error"`
	Detail string `json:"error_description"`
}

func (e *grantError) Error() string {
	if e.Detail != "" {
		return "xAI: " + e.Detail
	}
	if e.Code != "" {
		return "xAI: " + e.Code
	}
	return fmt.Sprintf("xAI answered %d", e.status)
}

// request asks xAI's token endpoint for tokens.
func (g *Grok) request(ctx context.Context, form url.Values) (token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := g.http.Do(req)
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
		Expires int    `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &got); err != nil || got.Access == "" {
		return token{}, errors.New("xAI sent a token den cannot read")
	}
	if got.Expires <= 0 {
		got.Expires = 3600
	}
	return token{Access: got.Access, Refresh: got.Refresh, Expiry: time.Now().Add(time.Duration(got.Expires) * time.Second)}, nil
}

// user asks xAI which account the token belongs to.
func (g *Grok) user(ctx context.Context, access string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userinfoURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+access)

	resp, err := g.http.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("xAI answered %d for the account", resp.StatusCode)
	}

	var info struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&info); err != nil {
		return "", "", err
	}
	// fx sends the account in a header.
	if info.Sub == "" || strings.ContainsAny(info.Sub, "\r\n") || len(info.Sub) > 256 {
		return "", "", errors.New("xAI named no account den can use")
	}
	return info.Sub, info.Email, nil
}

func (g *Grok) path() string { return filepath.Join(g.cfg.Secrets, File) }

func (g *Grok) load() (token, error) {
	var t token
	if !signin.Load(g.path(), &t) || t.Refresh == "" {
		return token{}, ErrNotConnected
	}
	return t, nil
}

func (g *Grok) save(t token) error {
	return signin.Save(g.path(), t)
}
