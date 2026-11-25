// Package push tells the user's phones of what comes while the app is
// closed: a reply, a job's result, a notice, or a question. It sends
// through Apple's push service once the user gives den a key for it.
package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/unsady/den/internal/atomicfile"
	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/events"
)

// The files the key lives in, among den's secrets: the .p8 key from the
// Apple Developer site, and what goes with it, such as
// {"key_id": "ABC123DEFG", "team_id": "DEF123GHIJ", "topic": "com.example.den"},
// where the topic is the app's bundle id.
const (
	KeyFile  = "apns.p8"
	InfoFile = "apns.json"
)

// devicesFile lists the phones to tell, among den's own files.
const devicesFile = "push_devices.json"

// Apple's push service, and its sandbox, which apps signed for
// development reach.
var (
	Production = "https://api.push.apple.com"
	Sandbox    = "https://api.sandbox.push.apple.com"
)

// device is a phone's token, and whether it takes pushes from the
// sandbox.
type device struct {
	Token   string `json:"token"`
	Sandbox bool   `json:"sandbox,omitempty"`
}

// Add keeps a phone's token, so it hears of what comes.
func Add(cfg config.Config, token string) error {
	token = strings.TrimSpace(token)
	if token == "" || strings.Trim(token, "0123456789abcdefABCDEF") != "" {
		return errors.New("the token is not a device token")
	}

	mu.Lock()
	defer mu.Unlock()

	list := load(cfg)
	if slices.ContainsFunc(list, func(d device) bool { return d.Token == token }) {
		return nil
	}
	return save(cfg, append(list, device{Token: token}))
}

// Forget drops every phone, so none hears of what comes until it gives its
// token again.
func Forget(cfg config.Config) error {
	mu.Lock()
	defer mu.Unlock()
	err := os.Remove(filepath.Join(cfg.State, devicesFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// mu guards the devices file.
var mu sync.Mutex

func load(cfg config.Config) []device {
	var list []device
	data, err := os.ReadFile(filepath.Join(cfg.State, devicesFile))
	if err == nil {
		json.Unmarshal(data, &list)
	}
	return list
}

func save(cfg config.Config, list []device) error {
	data, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(cfg.State, devicesFile), data, 0o600)
}

// Pusher sends den's events to the phones.
type Pusher struct {
	cfg    config.Config
	client *http.Client

	// jwt is the signed token Apple takes, made again before it gets old.
	signing sync.Mutex
	jwt     string
	made    time.Time
}

func New(cfg config.Config) *Pusher {
	return &Pusher{cfg: cfg, client: &http.Client{Timeout: 30 * time.Second}}
}

// Run sends each event worth telling until ctx ends.
func (p *Pusher) Run(ctx context.Context, hub *events.Hub) {
	list, stop := hub.Listen()
	defer func() { stop() }()
	for {
		select {
		case <-ctx.Done():
			return
		case e, open := <-list:
			// The hub dropped a listener that fell behind: listen again.
			if !open {
				stop()
				list, stop = hub.Listen()
				continue
			}
			// Apple may be slow: the hub drops a listener that falls behind.
			if n, ok := note(e); ok {
				go p.Send(ctx, n)
			}
		}
	}
}

// Note is what a push says.
type Note struct {
	Title, Body string
	// Session is where it came, which a tap opens, and Job the job of a
	// run's session, whose runs it opens.
	Session, Job string
	// Quiet comes without a sound.
	Quiet bool
}

// note is what e says as a push, and false for an event not worth one.
func note(e events.Event) (Note, bool) {
	n := Note{Title: e.Title, Body: e.Text, Session: e.Session, Job: e.Job, Quiet: e.Quiet}
	switch e.Type {
	case events.Reply, events.Result, events.Notice:
	case events.Ask:
		n.Body = e.What
	default:
		return n, false
	}
	if n.Title == "" {
		n.Title = "den"
	}
	n.Body = strings.Join(strings.Fields(n.Body), " ")
	if r := []rune(n.Body); len(r) > 200 {
		n.Body = string(r[:199]) + "…"
	}
	return n, n.Body != ""
}

// Send tells every phone of n. Without a key it does nothing.
func (p *Pusher) Send(ctx context.Context, n Note) {
	info, jwt, err := p.credentials()
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("push: %v", err)
		}
		return
	}

	mu.Lock()
	list := load(p.cfg)
	mu.Unlock()
	if len(list) == 0 {
		return
	}
	aps := map[string]any{
		"alert":     map[string]string{"title": n.Title, "body": n.Body},
		"thread-id": n.Session,
	}
	if !n.Quiet {
		aps["sound"] = "default"
	}
	payload, _ := json.Marshal(map[string]any{"aps": aps, "session": n.Session, "job": n.Job})

	// The posts go without the lock, so a phone that gives its token
	// meanwhile does not wait on Apple.
	gone := map[string]bool{}
	sandbox := map[string]bool{}
	for _, d := range list {
		was := d.Sandbox
		dropped, err := p.post(ctx, info.Topic, jwt, &d, payload)
		if err != nil {
			log.Printf("push: %v", err)
		}
		if dropped {
			gone[d.Token] = true
		} else if d.Sandbox != was {
			sandbox[d.Token] = d.Sandbox
		}
	}
	if len(gone) == 0 && len(sandbox) == 0 {
		return
	}

	mu.Lock()
	defer mu.Unlock()
	var kept []device
	for _, d := range load(p.cfg) {
		if gone[d.Token] {
			continue
		}
		if s, ok := sandbox[d.Token]; ok {
			d.Sandbox = s
		}
		kept = append(kept, d)
	}
	if err := save(p.cfg, kept); err != nil {
		log.Printf("push: %v", err)
	}
}

// post sends payload to d, and says whether d is gone for good. A token
// production refuses may be one for the sandbox, which d keeps once it
// works.
func (p *Pusher) post(ctx context.Context, topic, jwt string, d *device, payload []byte) (bool, error) {
	status, reason, err := p.request(ctx, d.Sandbox, topic, jwt, d.Token, payload)
	if err != nil {
		return false, err
	}
	if status == http.StatusBadRequest && reason == "BadDeviceToken" {
		status, reason, err = p.request(ctx, !d.Sandbox, topic, jwt, d.Token, payload)
		if err != nil {
			return false, err
		}
		if status == http.StatusOK {
			d.Sandbox = !d.Sandbox
		}
	}
	switch {
	case status == http.StatusOK:
		return false, nil
	case status == http.StatusGone, reason == "BadDeviceToken", reason == "Unregistered":
		return true, nil
	default:
		return false, fmt.Errorf("Apple refused a push: %d %s", status, reason)
	}
}

func (p *Pusher) request(ctx context.Context, sandbox bool, topic, jwt, token string, payload []byte) (int, string, error) {
	base := Production
	if sandbox {
		base = Sandbox
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/3/device/"+token, bytes.NewReader(payload))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("authorization", "bearer "+jwt)
	req.Header.Set("apns-topic", topic)
	req.Header.Set("apns-push-type", "alert")

	resp, err := p.client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	var why struct {
		Reason string `json:"reason"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	json.Unmarshal(data, &why)
	return resp.StatusCode, why.Reason, nil
}

// info is what goes with the key.
type info struct {
	KeyID  string `json:"key_id"`
	TeamID string `json:"team_id"`
	Topic  string `json:"topic"`
}

// credentials reads the key and signs a token with it, or takes the one
// signed within the last 40 minutes: Apple wants one newer than an hour,
// and refuses one made again more often than every 20 minutes.
func (p *Pusher) credentials() (info, string, error) {
	var i info
	data, err := os.ReadFile(filepath.Join(p.cfg.Secrets, InfoFile))
	if err != nil {
		return i, "", err
	}
	if err := json.Unmarshal(data, &i); err != nil || i.KeyID == "" || i.TeamID == "" || i.Topic == "" {
		return i, "", fmt.Errorf("%s needs key_id, team_id and topic", InfoFile)
	}

	p.signing.Lock()
	defer p.signing.Unlock()
	if p.jwt != "" && time.Since(p.made) < 40*time.Minute {
		return i, p.jwt, nil
	}

	pemData, err := os.ReadFile(filepath.Join(p.cfg.Secrets, KeyFile))
	if err != nil {
		return i, "", err
	}
	jwt, err := sign(pemData, i.KeyID, i.TeamID, time.Now())
	if err != nil {
		return i, "", err
	}
	p.jwt, p.made = jwt, time.Now()
	return i, jwt, nil
}

// sign makes the ES256 token Apple takes in place of a certificate.
func sign(pemData []byte, keyID, teamID string, now time.Time) (string, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return "", fmt.Errorf("%s is not a .p8 key", KeyFile)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("%s: %w", KeyFile, err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return "", fmt.Errorf("%s is not an ECDSA key", KeyFile)
	}

	enc := base64.RawURLEncoding
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": keyID})
	claims, _ := json.Marshal(map[string]any{"iss": teamID, "iat": now.Unix()})
	unsigned := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)

	sum := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return unsigned + "." + enc.EncodeToString(sig), nil
}
