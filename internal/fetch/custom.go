package fetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/unsady/den/internal/config"
)

// Services the user adds with den service add live in den's state, out of
// the agent's reach, and their tokens in den's secrets.

// CustomFile is the file in den's state with the user's services.
const CustomFile = "services.json"

// Ask modes say which requests to a service wait for the user.
const (
	AskWrites = "writes"
	AskNever  = "never"
	AskAlways = "always"
)

// Custom is a service the user added: an API den sends a token to.
type Custom struct {
	Name string `json:"name"`
	Host string `json:"host"`
	// Header carries the token as it is. Empty means Authorization with
	// Bearer before the token.
	Header string `json:"header,omitempty"`
	Ask    string `json:"ask"`
	// About tells the agent what the API does and where its docs are.
	About string `json:"about,omitempty"`
}

// SecretFile is the secret that holds the service's token.
func (c Custom) SecretFile() string { return c.Name + "_token" }

var (
	customName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	headerName = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
)

// reserved are names den itself uses.
var reserved = []string{"github", "google", "codex", "mail", "telegram", "provider"}

// NewCustom checks a service the user describes, from the API's address.
func NewCustom(name, address, header, ask, about string) (Custom, error) {
	if !customName.MatchString(name) {
		return Custom{}, fmt.Errorf("%q is not a service name: use lowercase letters, digits, and _", name)
	}
	if slices.Contains(reserved, name) || strings.HasPrefix(name, "provider_") {
		return Custom{}, fmt.Errorf("den uses the name %s itself: choose another", name)
	}
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return Custom{}, fmt.Errorf("%q is not an https address: den sends tokens only over https", address)
	}
	if header != "" && !headerName.MatchString(header) {
		return Custom{}, fmt.Errorf("%q is not a header name", header)
	}
	switch ask {
	case "":
		ask = AskWrites
	case AskWrites, AskNever, AskAlways:
	default:
		return Custom{}, fmt.Errorf("--ask takes writes, never, or always, not %q", ask)
	}
	return Custom{Name: name, Host: strings.ToLower(u.Hostname()), Header: header, Ask: ask, About: strings.TrimSpace(about)}, nil
}

// LoadCustom reads the user's services.
func LoadCustom(cfg config.Config) ([]Custom, error) {
	data, err := os.ReadFile(filepath.Join(cfg.State, CustomFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Custom
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("%s: %v", CustomFile, err)
	}
	return list, nil
}

// SaveCustom keeps the user's services, replacing the old list.
func SaveCustom(cfg config.Config, list []Custom) error {
	slices.SortFunc(list, func(a, b Custom) int { return strings.Compare(a.Name, b.Name) })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(cfg.State, CustomFile)
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// service is how den sends the custom service's token.
func (c Custom) service() service {
	return service{
		// A service that asks about every request asks every time.
		always: c.Ask != AskAlways,
		label:  c.Name,
		file:   c.SecretFile(),
		hosts:  []string{c.Host},
		authorize: func(r *http.Request, secret string) {
			if c.Header == "" {
				r.Header.Set("Authorization", "Bearer "+secret)
				return
			}
			r.Header.Set(c.Header, secret)
		},
		review: func(_ context.Context, _ *Fetcher, _ service, method string, _ *url.URL, _ []byte) string {
			switch {
			case c.Ask == AskNever:
				return ""
			case c.Ask == AskAlways:
				return "You asked to allow every request to " + c.Name + "."
			case method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions:
				return ""
			}
			return "It may change data on " + c.Host + "."
		},
	}
}

// lookup finds the service a secret name stands for: den's own, then the
// user's.
func (f *Fetcher) lookup(name string) (service, bool) {
	if s, ok := services[name]; ok {
		return s, true
	}
	list, err := LoadCustom(f.cfg)
	if err != nil {
		return service{}, false
	}
	for _, c := range list {
		if c.Name == name {
			return c.service(), true
		}
	}
	return service{}, false
}
