package grok

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/unsady/den/internal/config"
)

func TestSigninAndRefresh(t *testing.T) {
	var forms []url.Values
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/userinfo" {
			if r.Header.Get("Authorization") != "Bearer access-1" {
				http.Error(w, "no", http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"sub": "user-1", "email": "me@example.com"})
			return
		}
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		forms = append(forms, form)
		if status != http.StatusOK {
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		reply := map[string]any{"access_token": fmt.Sprintf("access-%d", len(forms)), "expires_in": 3600}
		if len(forms) == 1 {
			reply["refresh_token"] = "refresh-1"
		}
		json.NewEncoder(w).Encode(reply)
	}))
	defer server.Close()
	tokenURL = server.URL + "/token"
	userinfoURL = server.URL + "/userinfo"

	root := t.TempDir()
	home := filepath.Join(root, "agent", ".fx")
	cfg := config.Config{Secrets: filepath.Join(root, "secrets"), Sessions: filepath.Join(home, "sessions", "v2")}
	g := New(cfg)

	s := g.Begin()
	u, _ := url.Parse(s.URL)
	q := u.Query()
	if q.Get("redirect_uri") != Redirect || q.Get("code_challenge_method") != "S256" || q.Get("client_id") != clientID {
		t.Fatalf("sign-in address %s", s.URL)
	}
	if err := g.Deliver("not an address"); err != ErrNotSignin {
		t.Fatalf("deliver %v", err)
	}

	done := make(chan error, 1)
	var email string
	go func() {
		var err error
		email, err = g.Finish(context.Background(), s)
		done <- err
	}()
	for g.Deliver(Redirect+"?code=abc&state="+q.Get("state")) != nil {
		time.Sleep(10 * time.Millisecond)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if email != "me@example.com" || forms[0].Get("code") != "abc" || forms[0].Get("code_verifier") == "" || forms[0].Get("grant_type") != "authorization_code" {
		t.Fatalf("email %q, form %v", email, forms[0])
	}

	// fx gets a stand-in that never expires and the real account, never
	// the real token.
	data, _ := os.ReadFile(filepath.Join(home, authFile))
	var stand map[string]any
	json.Unmarshal(data, &stand)
	if stand["access_token"] != Stand || stand["account_id"] != "user-1" || stand["expires_at_ms"].(float64) < 4e12 {
		t.Fatalf("stand-in %s", data)
	}

	// A fresh token goes as it is; one about to expire is refreshed, and
	// keeps its refresh token when xAI sends none.
	if access, err := g.Token(context.Background()); err != nil || access != "access-1" || len(forms) != 1 {
		t.Fatalf("token %q %v, %d requests", access, err, len(forms))
	}
	tok, _ := g.load()
	tok.Expiry = time.Now().Add(time.Minute)
	g.save(tok)
	if access, err := g.Token(context.Background()); err != nil || access != "access-2" || forms[1].Get("refresh_token") != "refresh-1" {
		t.Fatalf("refresh %q %v %v", access, err, forms)
	}
	if tok, _ := g.load(); tok.Refresh != "refresh-1" || g.Account() != "user-1" {
		t.Fatalf("after refresh %+v", tok)
	}

	// A refused refresh means the account is gone.
	tok, _ = g.load()
	tok.Expiry = time.Now()
	g.save(tok)
	status = http.StatusBadRequest
	if _, err := g.Token(context.Background()); err != ErrNotConnected || g.Connected() {
		t.Fatalf("refused refresh %v", err)
	}

	g.stand("user-1")
	if err := g.Disconnect(); err != nil || g.Connected() {
		t.Fatalf("disconnect %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, authFile)); !os.IsNotExist(err) {
		t.Fatal("left fx's session")
	}
}
