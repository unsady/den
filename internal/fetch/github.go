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

// quiet are the writes in the user's own repositories that go without a
// question, by the path after /repos/<owner>/<repo>/: issues, pull
// requests, labels, and git objects that no branch points to yet. Anything
// else, such as files on a branch, merges, tags, releases, workflows, or
// access, waits for the user.
func quiet(method string, rest []string) bool {
	if method == http.MethodDelete || len(rest) == 0 {
		return false
	}
	switch rest[0] {
	case "issues", "labels", "milestones":
		return true
	case "pulls":
		// Merging, or updating a pull request's branch, moves a branch.
		last := rest[len(rest)-1]
		return last != "merge" && last != "update-branch"
	case "git":
		return method == http.MethodPost && len(rest) == 2 && (rest[1] == "blobs" || rest[1] == "trees" || rest[1] == "commits")
	}
	return false
}

// reviewGitHub lets reads and ordinary changes to the user's own
// repositories through, and asks about everything else.
func reviewGitHub(ctx context.Context, f *Fetcher, s service, method string, u *url.URL, body []byte) string {
	if method == http.MethodGet || method == http.MethodHead {
		return ""
	}
	path := strings.Trim(u.Path, "/")
	if path == "graphql" && method == http.MethodPost {
		var query string
		if !field(body, "query", &query) || !readOnly(query) {
			return "A GraphQL change."
		}
		return ""
	}

	parts := strings.Split(path, "/")
	if path == "user/repos" && method == http.MethodPost {
		private := false
		if field(body, "private", &private) && private {
			return ""
		}
		return "Creates a public repository."
	}
	login := f.login(ctx, s)
	if len(parts) < 3 || parts[0] != "repos" || login == "" || !strings.EqualFold(parts[1], login) {
		return "Writes outside your own repositories."
	}
	if len(parts) == 3 {
		return "Changes or deletes the repository itself."
	}
	if !quiet(method, parts[3:]) {
		return "Changes your repository beyond issues and pull requests."
	}
	return ""
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
