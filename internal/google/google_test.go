package google

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsady/den/internal/config"
)

// fake plays Google's token endpoint and Gmail.
type fake struct {
	t         *testing.T
	challenge string
	refreshes int
	revoked   bool
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	switch r.URL.Path {
	case "/token":
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
			if r.PostForm.Get("code") != "4/code" || base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge || r.PostForm.Get("client_secret") != "shh" {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			w.Write([]byte(`{"access_token":"a1","refresh_token":"r1","expires_in":3600,"scope":"` + strings.Join(Scopes, " ") + `"}`))
		case "refresh_token":
			if f.revoked {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`))
				return
			}
			f.refreshes++
			w.Write([]byte(`{"access_token":"a2","expires_in":3600}`))
		}

	case "/profile":
		if r.Header.Get("Authorization") != "Bearer a1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"emailAddress":"me@example.com"}`))
	}
}

func setup(t *testing.T) (*Google, *fake) {
	t.Helper()

	f := &fake{t: t}
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	tokenURL, profileURL = server.URL+"/token", server.URL+"/profile"

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ClientFile), []byte(`{"web":{"client_id":"id","client_secret":"shh"}}`), 0o600)

	return New(config.Config{Secrets: dir}), f
}

func TestSignIn(t *testing.T) {
	g, f := setup(t)

	s, err := g.Begin()
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(s.URL)
	q := u.Query()
	if q.Get("redirect_uri") != Redirect || q.Get("code_challenge_method") != "S256" || q.Get("access_type") != "offline" {
		t.Fatalf("sign-in URL: %s", s.URL)
	}
	f.challenge = q.Get("code_challenge")

	type result struct {
		email string
		err   error
	}
	done := make(chan result)
	go func() {
		email, err := g.Finish(context.Background(), s)
		done <- result{email, err}
	}()

	if err := g.Deliver("hello"); !errors.Is(err, ErrNotSignin) {
		t.Fatalf("plain text: %v", err)
	}
	if err := g.Deliver(Redirect + "?state=other&code=x"); err == nil {
		t.Fatal("took another state")
	}
	link := Redirect + "?state=" + q.Get("state") + "&code=4/code&scope=x"
	for g.Deliver("here: "+link) != nil {
		time.Sleep(time.Millisecond)
	}

	got := <-done
	email, err := got.email, got.err
	if err != nil || email != "me@example.com" {
		t.Fatalf("finish: %q %v", email, err)
	}
	if g.Account() != "me@example.com" {
		t.Fatalf("account: %q", g.Account())
	}

	info, _ := os.Stat(filepath.Join(g.cfg.Secrets, TokenFile))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode %v", info.Mode())
	}

	if token, err := g.Token(context.Background()); err != nil || token != "a1" {
		t.Fatalf("token: %q %v", token, err)
	}
	if f.refreshes != 0 {
		t.Fatal("refreshed a fresh token")
	}
}

func TestTokenRefreshes(t *testing.T) {
	g, f := setup(t)
	g.save(token{Refresh: "r1", Access: "old", Expiry: time.Now()})

	if token, err := g.Token(context.Background()); err != nil || token != "a2" {
		t.Fatalf("token: %q %v", token, err)
	}
	if token, _ := g.Token(context.Background()); token != "a2" || f.refreshes != 1 {
		t.Fatalf("second token: %q after %d refreshes", token, f.refreshes)
	}

	saved, _ := g.load()
	if saved.Refresh != "r1" {
		t.Fatalf("lost the refresh token: %+v", saved)
	}
}

func TestRevokedAccountIsForgotten(t *testing.T) {
	g, f := setup(t)
	g.save(token{Refresh: "r1"})
	f.revoked = true

	if _, err := g.Token(context.Background()); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("token: %v", err)
	}
	if g.Account() != "" {
		t.Fatal("still connected")
	}
}

func TestMissingClient(t *testing.T) {
	g := New(config.Config{Secrets: t.TempDir()})

	if _, err := g.Begin(); !errors.Is(err, ErrClient) {
		t.Fatalf("begin: %v", err)
	}
}

func TestSignInTimesOut(t *testing.T) {
	g, _ := setup(t)
	s, _ := g.Begin()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := g.Finish(ctx, s); err == nil {
		t.Fatal("finished without a code")
	}
}
