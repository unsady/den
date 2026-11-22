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

// fake plays Google's token endpoint.
type fake struct {
	t         *testing.T
	challenge string
	refreshes int
	revoked   bool
	// granted are the scopes the user allowed on Google's page.
	granted string
	// revokes are the refresh tokens den revoked.
	revokes []string
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	switch r.URL.Path {
	case "/revoke":
		f.revokes = append(f.revokes, r.PostForm.Get("token"))

	case "/token":
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
			if r.PostForm.Get("code") != "4/code" || base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge || r.PostForm.Get("client_secret") != "shh" {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			id := "x." + base64.RawURLEncoding.EncodeToString([]byte(`{"email":"me@example.com"}`)) + ".sig"
			w.Write([]byte(`{"access_token":"a1","refresh_token":"r1","expires_in":3600,"scope":"` + f.granted + `","id_token":"` + id + `"}`))
		case "refresh_token":
			if f.revoked {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`))
				return
			}
			f.refreshes++
			w.Write([]byte(`{"access_token":"a2","expires_in":3600}`))
		}
	}
}

func setup(t *testing.T) (*Google, *fake) {
	t.Helper()

	f := &fake{t: t, granted: strings.Join(Scopes, " ")}
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	tokenURL, revokeURL = server.URL+"/token", server.URL+"/revoke"

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ClientFile), []byte(`{"web":{"client_id":"id","client_secret":"shh"}}`), 0o600)

	return New(config.Config{Secrets: dir}), f
}

// signIn signs in, with the user allowing what f grants.
func signIn(t *testing.T, g *Google, f *fake) (string, error) {
	t.Helper()

	s, err := g.Begin()
	if err != nil {
		t.Fatal(err)
	}
	f.challenge = challenge(s.URL)
	done := make(chan error, 1)
	var email string
	go func() {
		var err error
		email, err = g.Finish(context.Background(), s)
		done <- err
	}()
	u, _ := url.Parse(s.URL)
	link := Redirect + "?state=" + u.Query().Get("state") + "&code=4/code"
	for g.Deliver(link) != nil {
		time.Sleep(time.Millisecond)
	}
	err = <-done
	return email, err
}

func challenge(link string) string {
	u, _ := url.Parse(link)
	return u.Query().Get("code_challenge")
}

func TestSomePermissions(t *testing.T) {
	g, f := setup(t)

	// The user allows Drive alone.
	f.granted = "openid https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/drive"
	if email, err := signIn(t, g, f); err != nil || email != "me@example.com" {
		t.Fatalf("finish: %q %v", email, err)
	}
	if got := g.Granted(); len(got) != 1 || got[0] != "Drive" {
		t.Fatalf("granted %q", got)
	}

	// The user allows none of the APIs.
	g.Disconnect(context.Background())
	f.granted = "openid https://www.googleapis.com/auth/userinfo.email"
	if _, err := signIn(t, g, f); err == nil {
		t.Fatal("connected with nothing granted")
	}
	if g.Granted() != nil {
		t.Fatal("kept an account with nothing granted")
	}
	if len(f.revokes) != 2 || f.revokes[1] != "r1" {
		t.Fatalf("revoked %q", f.revokes)
	}

	// Granting nothing again keeps an account that works: Google would
	// revoke its grant too.
	f.granted = strings.Join(Scopes, " ")
	if _, err := signIn(t, g, f); err != nil {
		t.Fatal(err)
	}
	f.granted = "openid"
	if _, err := signIn(t, g, f); err == nil {
		t.Fatal("connected with nothing granted")
	}
	if len(f.revokes) != 2 || len(g.Granted()) != 3 {
		t.Fatalf("revoked %q, granted %q", f.revokes, g.Granted())
	}
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
