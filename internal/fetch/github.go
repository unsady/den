package fetch

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
)

var github = service{
	label:  "GitHub",
	always: true,
	file:   "github_token",
	hosts:  []string{"api.github.com", "uploads.github.com"},
	// Archives and release files redirect to these hosts with a short-lived
	// link of their own.
	follow: []string{"codeload.github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com"},
	authorize: func(r *http.Request, token string) {
		r.Header.Set("Authorization", "Bearer "+token)
		if r.Header.Get("Accept") == "" {
			r.Header.Set("Accept", "application/vnd.github+json")
		}
		r.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	},
	review: reviewGitHub,
}

// guarded are the parts of the user's own repository whose changes need the
// user's approval: they grant access, send data elsewhere, or rewrite
// history.
var guarded = map[string]bool{
	"collaborators": true, "invitations": true, "keys": true, "hooks": true,
	"actions": true, "environments": true, "rulesets": true, "branches": true,
}

// reviewGitHub lets reads and ordinary changes to the user's own
// repositories through, and asks about everything else.
func reviewGitHub(ctx context.Context, f *Fetcher, s service, method string, u *url.URL, body []byte) string {
	if method == http.MethodGet || method == http.MethodHead {
		return ""
	}
	path := strings.Trim(u.Path, "/")
	if path == "graphql" && method == http.MethodPost {
		if mutation(body) {
			return "A GraphQL change."
		}
		return ""
	}

	parts := strings.Split(path, "/")
	if path == "user/repos" && method == http.MethodPost {
		if private(body) {
			return ""
		}
		return "Creates a public repository."
	}
	login := f.login(ctx, s)
	if len(parts) < 3 || parts[0] != "repos" || login == "" || !strings.EqualFold(parts[1], login) {
		return "Writes outside your own repositories."
	}
	switch {
	case len(parts) == 3:
		return "Changes or deletes the repository itself."
	case guarded[parts[3]]:
		return "Changes who or what can access the repository."
	case parts[3] == "git" && method != http.MethodPost:
		return "Moves or deletes a branch or tag directly."
	}
	return ""
}

func mutation(body []byte) bool {
	var q struct {
		Query string `json:"query"`
	}
	if json.Unmarshal(body, &q) != nil {
		return true
	}
	query := strings.TrimSpace(q.Query)
	return !strings.HasPrefix(query, "{") && !strings.HasPrefix(query, "query")
}

func private(body []byte) bool {
	var r struct {
		Private bool `json:"private"`
	}
	return json.Unmarshal(body, &r) == nil && r.Private
}

// login returns the GitHub account the token belongs to, or "" when GitHub
// does not say.
func (f *Fetcher) login(ctx context.Context, s service) string {
	token, err := f.secret(ctx, s)
	if err != nil {
		return ""
	}
	f.mu.Lock()
	login, ok := f.logins[token]
	f.mu.Unlock()
	if ok {
		return login
	}

	r, err := http.NewRequestWithContext(ctx, http.MethodGet, apiGitHub+"/user", nil)
	if err != nil {
		return ""
	}
	s.authorize(r, token)
	resp, err := f.client.Do(r.WithContext(context.WithValue(ctx, serviceKey{}, s)))
	if err != nil {
		log.Printf("fetch: github login: %v", err)
		return ""
	}
	defer resp.Body.Close()
	var user struct {
		Login string `json:"login"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&user) != nil {
		return ""
	}

	f.mu.Lock()
	f.logins[token] = user.Login
	f.mu.Unlock()
	return user.Login
}

// apiGitHub is GitHub's API. Tests point it at a fake.
var apiGitHub = "https://api.github.com"
