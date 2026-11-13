// Package signin holds what den's browser sign-ins share: PKCE, the
// sign-ins waiting for the address the browser came back to, and tokens
// kept readable by den alone.
package signin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrNotSignin means a line is not a sign-in's address, so a chat message
// can go on to the agent.
var ErrNotSignin = errors.New("not a sign-in address")

// Random returns a fresh secret for a state or a PKCE verifier.
func Random() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Challenge is the PKCE S256 challenge for verifier.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Pending are the sign-ins waiting for their address, by state.
type Pending struct {
	// Who names the service in errors, such as "Google".
	Who string
	// Redirect is the address the service sends the browser back to.
	Redirect string
	// Again is the command that starts a new sign-in.
	Again string

	mu      sync.Mutex
	waiting map[string]chan url.Values
}

// Wait waits for Deliver to bring the address that carries state, and
// returns its code.
func (p *Pending) Wait(ctx context.Context, state string) (string, error) {
	ch := make(chan url.Values, 1)
	p.mu.Lock()
	if p.waiting == nil {
		p.waiting = map[string]chan url.Values{}
	}
	p.waiting[state] = ch
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.waiting, state)
		p.mu.Unlock()
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
			return "", fmt.Errorf("%s answered %s", p.Who, e)
		case q.Get("code") == "":
			return "", errors.New("the address has no code: send the whole address")
		}
		return q.Get("code"), nil
	}
}

// Deliver hands a waiting sign-in the address the browser came back to.
// It returns ErrNotSignin when text holds no such address.
func (p *Pending) Deliver(text string) error {
	link := ""
	for _, field := range strings.Fields(text) {
		if strings.HasPrefix(field, p.Redirect) {
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

	p.mu.Lock()
	ch, ok := p.waiting[q.Get("state")]
	p.mu.Unlock()
	if !ok {
		return fmt.Errorf("this sign-in has expired: run %s again", p.Again)
	}
	select {
	case ch <- q:
	default:
	}
	return nil
}

// Load reads tokens den saved at path into v. It reports false when there
// are none.
func Load(path string, v any) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return json.Unmarshal(data, v) == nil
}

// Save writes v to path, readable by den alone, replacing the file whole.
func Save(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}
