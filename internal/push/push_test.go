package push

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/events"
)

func testConfig(t *testing.T) config.Config {
	dir := t.TempDir()
	cfg := config.Config{State: dir, Secrets: filepath.Join(dir, "secrets")}
	if err := os.MkdirAll(cfg.Secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func newKey(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func TestSign(t *testing.T) {
	key, p8 := newKey(t)
	jwt, err := sign(p8, "KEY123", "TEAM45", time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}

	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt = %q", jwt)
	}
	enc := base64.RawURLEncoding
	header, _ := enc.DecodeString(parts[0])
	claims, _ := enc.DecodeString(parts[1])
	if string(header) != `{"alg":"ES256","kid":"KEY123"}` || string(claims) != `{"iat":1700000000,"iss":"TEAM45"}` {
		t.Errorf("header %s, claims %s", header, claims)
	}
	sig, _ := enc.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if len(sig) != 64 || !ecdsa.Verify(&key.PublicKey, sum[:], r, s) {
		t.Error("the signature does not verify")
	}
}

func TestNote(t *testing.T) {
	cases := []struct {
		e    events.Event
		want Note
		ok   bool
	}{
		{events.Event{Type: events.Reply, Session: "s1", Text: "Done\n\nall  of it"}, Note{Title: "den", Body: "Done all of it", Session: "s1"}, true},
		{events.Event{Type: events.Result, Session: "j1", Title: "Weather", Job: "weather", Text: "Sunny", Quiet: true}, Note{Title: "Weather", Body: "Sunny", Session: "j1", Job: "weather", Quiet: true}, true},
		{events.Event{Type: events.Result, Session: "j2", Title: "Tidy", Job: "nightly-tidy", Text: "Folded the notes", Muted: true}, Note{}, false},
		{events.Event{Type: events.Ask, What: "Send the mail"}, Note{Title: "den", Body: "Send the mail"}, true},
		{events.Event{Type: events.Step, Session: "s1", Text: "Reading"}, Note{}, false},
		{events.Event{Type: events.Reply, Session: "s1"}, Note{}, false},
	}
	for _, c := range cases {
		got, ok := note(c.e)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("note(%v) = %v, %v; want %v, %v", c.e, got, ok, c.want, c.ok)
		}
	}
	if long, _ := note(events.Event{Type: events.Reply, Text: strings.Repeat("я", 300)}); len([]rune(long.Body)) != 200 {
		t.Errorf("a long body has %d runes", len([]rune(long.Body)))
	}
}

func TestSend(t *testing.T) {
	cfg := testConfig(t)
	p := New(cfg)

	// Without a key nothing goes.
	if err := Add(cfg, "aa11"); err != nil {
		t.Fatal(err)
	}
	if err := Add(cfg, "not a token"); err == nil {
		t.Error("a bad token is kept")
	}
	p.Send(t.Context(), Note{Title: "den", Body: "Done"})

	_, p8 := newKey(t)
	os.WriteFile(filepath.Join(cfg.Secrets, KeyFile), p8, 0o600)
	os.WriteFile(filepath.Join(cfg.Secrets, InfoFile), []byte(`{"key_id":"K","team_id":"T","topic":"com.example.den"}`), 0o600)

	// Production knows aa11, the sandbox bb22; cc33 is gone.
	var got []string
	sound := "default"
	server := func(name string, known string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := strings.TrimPrefix(r.URL.Path, "/3/device/")
			body, _ := io.ReadAll(r.Body)
			got = append(got, name+" "+token)
			if r.Header.Get("apns-topic") != "com.example.den" || !strings.HasPrefix(r.Header.Get("authorization"), "bearer ") {
				t.Errorf("headers %v", r.Header)
			}
			var payload struct {
				Aps struct {
					Sound string `json:"sound"`
				} `json:"aps"`
				Session string `json:"session"`
				Job     string `json:"job"`
			}
			json.Unmarshal(body, &payload)
			if payload.Session != "s1" || payload.Job != "weather" || payload.Aps.Sound != sound {
				t.Errorf("payload %s", body)
			}
			switch {
			case token == known:
			case token == "cc33" && name == "production":
				w.WriteHeader(http.StatusGone)
				w.Write([]byte(`{"reason":"Unregistered"}`))
			default:
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"reason":"BadDeviceToken"}`))
			}
		}))
	}
	prod, sandbox := server("production", "aa11"), server("sandbox", "bb22")
	defer prod.Close()
	defer sandbox.Close()
	Production, Sandbox = prod.URL, sandbox.URL
	defer func() { Production, Sandbox = "https://api.push.apple.com", "https://api.sandbox.push.apple.com" }()

	Add(cfg, "bb22")
	Add(cfg, "cc33")
	p.Send(t.Context(), Note{Title: "den", Body: "Done", Session: "s1", Job: "weather"})
	want := "production aa11,production bb22,sandbox bb22,production cc33"
	if strings.Join(got, ",") != want {
		t.Errorf("sent %v, want %s", got, want)
	}

	// bb22 goes to the sandbox from now on, and cc33 is dropped. A quiet
	// one has no sound.
	got, sound = nil, ""
	p.Send(t.Context(), Note{Title: "den", Body: "Again", Session: "s1", Job: "weather", Quiet: true})
	if want := "production aa11,sandbox bb22"; strings.Join(got, ",") != want {
		t.Errorf("sent %v, want %s", got, want)
	}
}
