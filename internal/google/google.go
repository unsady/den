// Package google connects den to the user's Google account and keeps its
// access token fresh.
//
// The user creates an OAuth client of type Desktop app in their own Google
// Cloud project and writes its JSON as the secret google_client. den opens
// the sign-in with PKCE and a random state, and Google sends the browser
// back to a loopback address, where den on the user's computer catches it and
// hands it to Deliver; from another device, the user pastes the address. den then exchanges the one-time code itself. The agent never
// sees a token: it reaches Google only through den fetch.
package google

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
	"time"

	"github.com/unsady/den/internal/config"
)

const (
	// ClientFile is the OAuth client JSON from Google Cloud's console.
	ClientFile = "google_client"
	// TokenFile is where den keeps the account's tokens.
	TokenFile = "google_token"

	// Wait is how long den waits for the user to finish signing in.
	Wait = 15 * time.Minute

	// Redirect is where Google sends the browser after the sign-in. A
	// Desktop app client may redirect only to a loopback address.
	Redirect = "http://127.0.0.1:8941/den"
)

// Scopes are mail without permanent deletion or settings, and calendar
// events.
var Scopes = []string{
	"https://www.googleapis.com/auth/gmail.modify",
	"https://www.googleapis.com/auth/calendar.events",
}

var (
	authURL   = "https://accounts.google.com/o/oauth2/v2/auth"
	tokenURL  = "https://oauth2.googleapis.com/token"
	revokeURL = "https://oauth2.googleapis.com/revoke"
	// profileURL names the connected account.
	profileURL = "https://gmail.googleapis.com/gmail/v1/users/me/profile"
)

// ErrClient means the user has not added the OAuth client yet.
var ErrClient = errors.New("Google is not set up: the user connects it with `den connect google` on their computer")

// ErrNotSignin means a message is not a sign-in's address.
var ErrNotSignin = errors.New("not a sign-in address")

// ErrNotConnected means there is no account, or Google revoked it.
var ErrNotConnected = errors.New("Google is not connected: the user connects it with `den connect google` on their computer")

type client struct {
	ID     string `json:"client_id"`
	Secret string `json:"client_secret"`
}

type token struct {
	Refresh string    `json:"refresh_token"`
	Access  string    `json:"access_token,omitempty"`
	Expiry  time.Time `json:"expiry,omitzero"`
	Scope   string    `json:"scope,omitempty"`
	Email   string    `json:"email,omitempty"`
}

type Google struct {
	cfg  config.Config
	http *http.Client

	mu sync.Mutex
	// pending are the sign-ins waiting for their address, by state.
	pending map[string]chan url.Values
}

func New(cfg config.Config) *Google {
	return &Google{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second}, pending: map[string]chan url.Values{}}
}

// Signin is one sign-in the user has yet to finish.
type Signin struct {
	// URL is where the user signs in.
	URL string

	client   client
	state    string
	verifier string
	redirect string
}

// Begin starts a sign-in.
func (g *Google) Begin() (*Signin, error) {
	c, err := g.client()
	if err != nil {
		return nil, err
	}

	s := &Signin{client: c, state: random(), verifier: random(), redirect: Redirect}
	challenge := sha256.Sum256([]byte(s.verifier))
	q := url.Values{
		"client_id":             {c.ID},
		"redirect_uri":          {s.redirect},
		"response_type":         {"code"},
		"scope":                 {strings.Join(Scopes, " ")},
		"state":                 {s.state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
		"access_type":           {"offline"},
		// Google sends a refresh token only on consent.
		"prompt": {"consent"},
	}
	s.URL = authURL + "?" + q.Encode()
	return s, nil
}

// Finish waits for the user to sign in, keeps the account's tokens, and
// returns its address.
func (g *Google) Finish(ctx context.Context, s *Signin) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, Wait)
	defer cancel()

	code, err := g.wait(ctx, s.state)
	if err != nil {
		return "", err
	}

	t, err := g.request(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {s.verifier},
		"redirect_uri":  {s.redirect},
		"client_id":     {s.client.ID},
		"client_secret": {s.client.Secret},
	})
	if err != nil {
		return "", err
	}
	if t.Refresh == "" {
		return "", errors.New("Google sent no refresh token")
	}
	for _, scope := range Scopes {
		if !strings.Contains(" "+t.Scope+" ", " "+scope+" ") {
			return "", errors.New("not every permission was granted: allow mail and calendar on Google's page")
		}
	}

	t.Email = g.email(ctx, t.Access)

	g.mu.Lock()
	defer g.mu.Unlock()
	return t.Email, g.save(t)
}

// wait waits for Deliver to bring the address Google sent state to.
func (g *Google) wait(ctx context.Context, state string) (string, error) {
	ch := make(chan url.Values, 1)
	g.mu.Lock()
	g.pending[state] = ch
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		delete(g.pending, state)
		g.mu.Unlock()
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
			return "", fmt.Errorf("Google answered %s", e)
		case q.Get("code") == "":
			return "", errors.New("the address has no code: send the whole address")
		}
		return q.Get("code"), nil
	}
}

// Deliver hands a sign-in the address that the user sent from the browser.
// It returns ErrNotSignin when text is not such an address, so the message
// can go on to the agent, and an error when the sign-in is no longer
// waiting.
func (g *Google) Deliver(text string) error {
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
		return errors.New("this address is cut: send the whole address")
	}
	q := u.Query()

	g.mu.Lock()
	ch, ok := g.pending[q.Get("state")]
	g.mu.Unlock()
	if !ok {
		return errors.New("this sign-in has expired: run den connect google again")
	}
	select {
	case ch <- q:
	default:
	}
	return nil
}

// Token returns an access token for the connected account, refreshed when
// it is about to expire.
func (g *Google) Token(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	t, err := g.load()
	if err != nil {
		return "", err
	}
	if t.Access != "" && time.Until(t.Expiry) > time.Minute {
		return t.Access, nil
	}

	c, err := g.client()
	if err != nil {
		return "", err
	}
	fresh, err := g.request(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {t.Refresh},
		"client_id":     {c.ID},
		"client_secret": {c.Secret},
	})
	var revoked *grantError
	if errors.As(err, &revoked) && revoked.Code == "invalid_grant" {
		// The user removed den's access, or Google expired it.
		os.Remove(g.path(TokenFile))
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

// Account returns the connected address, or "" when none is connected.
func (g *Google) Account() string {
	g.mu.Lock()
	defer g.mu.Unlock()

	t, err := g.load()
	if err != nil {
		return ""
	}
	if t.Email == "" {
		return "your account"
	}
	return t.Email
}

// Disconnect revokes den's access and forgets the account.
func (g *Google) Disconnect(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	t, err := g.load()
	if err != nil {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, revokeURL, strings.NewReader(url.Values{"token": {t.Refresh}}.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if resp, err := g.http.Do(req); err == nil {
		resp.Body.Close()
	}
	// The tokens go even if Google did not answer: den no longer uses them.
	return os.Remove(g.path(TokenFile))
}

type grantError struct {
	Code        string `json:"error"`
	Description string `json:"error_description"`
}

func (e *grantError) Error() string {
	if e.Description != "" {
		return "Google: " + e.Description
	}
	return "Google: " + e.Code
}

// request asks Google's token endpoint for tokens.
func (g *Google) request(ctx context.Context, form url.Values) (token, error) {
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

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return token{}, err
	}
	if resp.StatusCode != http.StatusOK {
		e := &grantError{}
		if json.Unmarshal(body, e) != nil || e.Code == "" {
			return token{}, fmt.Errorf("Google answered %s", resp.Status)
		}
		return token{}, e
	}

	var got struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Expires int    `json:"expires_in"`
		Scope   string `json:"scope"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		return token{}, err
	}
	return token{
		Access:  got.Access,
		Refresh: got.Refresh,
		Expiry:  time.Now().Add(time.Duration(got.Expires) * time.Second),
		Scope:   got.Scope,
	}, nil
}

// email asks Gmail which address the token belongs to.
func (g *Google) email(ctx context.Context, access string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, profileURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+access)

	resp, err := g.http.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	var profile struct {
		Email string `json:"emailAddress"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&profile)
	return profile.Email
}

// client reads the OAuth client JSON that Google Cloud's console downloads,
// for a web application or a desktop app.
func (g *Google) client() (client, error) {
	data, err := os.ReadFile(g.path(ClientFile))
	if err != nil {
		return client{}, ErrClient
	}

	var file struct {
		Web       *client `json:"web"`
		Installed *client `json:"installed"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return client{}, fmt.Errorf("%s is not the client JSON from Google Cloud: %v", ClientFile, err)
	}
	c := file.Web
	if c == nil {
		c = file.Installed
	}
	if c == nil || c.ID == "" || c.Secret == "" {
		return client{}, fmt.Errorf("%s has no client_id and client_secret", ClientFile)
	}
	return *c, nil
}

func (g *Google) load() (token, error) {
	data, err := os.ReadFile(g.path(TokenFile))
	if err != nil {
		return token{}, ErrNotConnected
	}
	var t token
	if err := json.Unmarshal(data, &t); err != nil || t.Refresh == "" {
		return token{}, ErrNotConnected
	}
	return t, nil
}

// save writes the tokens readable by den alone.
func (g *Google) save(t token) error {
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(g.cfg.Secrets, 0o700); err != nil {
		return err
	}

	tmp := g.path(TokenFile + ".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, g.path(TokenFile))
}

func (g *Google) path(name string) string {
	return filepath.Join(g.cfg.Secrets, name)
}

func random() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
