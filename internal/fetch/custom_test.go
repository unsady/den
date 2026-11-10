package fetch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsady/den/internal/approve"
	"github.com/unsady/den/internal/config"
)

func TestNewCustom(t *testing.T) {
	s, err := NewCustom("crm", "https://CRM.example.com/api", "", "", "Events API")
	if err != nil || s.Host != "crm.example.com" || s.Ask != AskWrites {
		t.Fatalf("%+v %v", s, err)
	}
	for _, c := range [][5]string{
		{"CRM", "https://a.example", "", "", ""},
		{"github", "https://a.example", "", "", ""},
		{"crm", "http://a.example", "", "", ""},
		{"crm", "https://a.example", "Bad Header", "", ""},
		{"crm", "https://a.example", "", "sometimes", ""},
	} {
		if _, err := NewCustom(c[0], c[1], c[2], c[3], c[4]); err == nil {
			t.Errorf("took %v", c)
		}
	}
}

func TestCustomService(t *testing.T) {
	var got []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.Header.Get("Authorization")+" "+r.Header.Get("X-Api-Key"))
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	dir := t.TempDir()
	cfg := config.Config{Secrets: filepath.Join(dir, "secrets"), State: filepath.Join(dir, "state")}
	os.MkdirAll(cfg.Secrets, 0o700)
	os.MkdirAll(cfg.State, 0o700)
	crm, _ := NewCustom("crm", server.URL, "", "", "")
	keyed, _ := NewCustom("keyed", server.URL, "X-Api-Key", AskNever, "")
	if err := SaveCustom(cfg, []Custom{crm, keyed}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(cfg.Secrets, "crm_token"), []byte("tok\n"), 0o600)
	os.WriteFile(filepath.Join(cfg.Secrets, "keyed_token"), []byte("key\n"), 0o600)

	var asked []string
	f := New(cfg, func(ctx context.Context, q approve.Question) error {
		asked = append(asked, q.Text)
		return errors.New("denied")
	}, nil)
	f.client.Transport = server.Client().Transport

	// A read goes at once with the token; a write waits for the user.
	if _, err := f.Do(context.Background(), Request{Secret: "crm", URL: server.URL + "/api/events", Header: map[string]string{"Authorization": "stolen"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Do(context.Background(), Request{Secret: "crm", Method: "POST", URL: server.URL + "/api/events", Body: []byte(`{"title":"x"}`)}); err == nil {
		t.Fatal("a denied write went out")
	}
	if len(asked) != 1 || !strings.HasPrefix(asked[0], "crm: POST /api/events\nIt may change data on 127.0.0.1.") {
		t.Fatalf("asked %q", asked)
	}

	// A service that never asks sends its token in its own header.
	if _, err := f.Do(context.Background(), Request{Secret: "keyed", Method: "DELETE", URL: server.URL + "/x"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "|") != "GET Bearer tok |DELETE  key" {
		t.Fatalf("sent %q", got)
	}

	if _, err := f.Do(context.Background(), Request{Secret: "crm", URL: "https://evil.example/x"}); !errors.Is(err, ErrRequest) {
		t.Fatalf("other host: %v", err)
	}
}
