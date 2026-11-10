package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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

func jwt(claims map[string]any) string {
	data, _ := json.Marshal(claims)
	return "e30." + base64.RawURLEncoding.EncodeToString(data) + ".sig"
}

func TestSigninAndRefresh(t *testing.T) {
	var forms []url.Values
	var bodies []string
	expires := time.Now().Add(time.Hour)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Content-Type") == "application/json" {
			bodies = append(bodies, string(body))
		} else {
			form, _ := url.ParseQuery(string(body))
			forms = append(forms, form)
		}
		access := jwt(map[string]any{"exp": expires.Unix(), "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "acct-1"}})
		json.NewEncoder(w).Encode(map[string]string{
			"access_token":  access,
			"refresh_token": fmt.Sprintf("refresh-%d", len(forms)+len(bodies)),
			"id_token":      jwt(map[string]any{"email": "me@example.com"}),
		})
	}))
	defer server.Close()
	tokenURL = server.URL

	root := t.TempDir()
	home := filepath.Join(root, "agent", ".fx")
	cfg := config.Config{Secrets: filepath.Join(root, "secrets"), Sessions: filepath.Join(home, "sessions", "v2")}
	c := New(cfg)

	s := c.Begin()
	u, _ := url.Parse(s.URL)
	if u.Query().Get("redirect_uri") != Redirect || u.Query().Get("code_challenge_method") != "S256" {
		t.Fatalf("sign-in address %s", s.URL)
	}
	if err := c.Deliver("not an address"); err != ErrNotSignin {
		t.Fatalf("deliver %v", err)
	}

	done := make(chan error, 1)
	var email string
	go func() {
		var err error
		email, err = c.Finish(context.Background(), s)
		done <- err
	}()
	for c.Deliver(Redirect+"?code=abc&state="+u.Query().Get("state")) != nil {
		time.Sleep(10 * time.Millisecond)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if email != "me@example.com" || forms[0].Get("code") != "abc" || forms[0].Get("code_verifier") == "" {
		t.Fatalf("email %q, form %v", email, forms[0])
	}

	// fx gets a stand-in that never expires, never the real token.
	data, _ := os.ReadFile(filepath.Join(home, authFile))
	var stand map[string]any
	json.Unmarshal(data, &stand)
	if stand["access_token"] != Stand("acct-1") || stand["account_id"] != "acct-1" || stand["expires_at_ms"].(float64) < 4e12 {
		t.Fatalf("stand-in %s", data)
	}

	// A fresh token goes as it is; one about to expire is refreshed.
	access, account, err := c.Token(context.Background())
	if err != nil || account != "acct-1" || !strings.HasPrefix(access, "e30.") || len(bodies) != 0 {
		t.Fatalf("token %q %q %v, refreshed %d times", access, account, err, len(bodies))
	}
	expires = time.Now().Add(time.Minute)
	c.Token(context.Background())
	if len(bodies) != 0 {
		t.Fatal("refreshed a token an hour from expiry")
	}
	tok, _ := c.load()
	tok.Expiry = time.Now().Add(time.Minute)
	c.save(tok)
	if _, _, err := c.Token(context.Background()); err != nil || len(bodies) != 1 || !strings.Contains(bodies[0], `"refresh_token":"refresh-1"`) {
		t.Fatalf("refresh %v %q", err, bodies)
	}

	if err := c.Disconnect(); err != nil || c.Connected() {
		t.Fatalf("disconnect %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, authFile)); !os.IsNotExist(err) {
		t.Fatal("left fx's session")
	}
}
