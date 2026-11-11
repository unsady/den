package fetch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/unsady/den/internal/approve"
	"github.com/unsady/den/internal/config"
)

func TestFetch(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user" {
			_, _ = io.WriteString(w, `{"login":"Me"}`)
			return
		}
		if r.URL.Path == "/away" {
			http.Redirect(w, r, "https://elsewhere.example/x", http.StatusFound)
			return
		}
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Link", `<next>; rel="next"`)
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	gh := github
	gh.hosts = []string{"127.0.0.1"}
	services["github"] = gh
	defer func() { services["github"] = github }()
	apiGitHub = server.URL

	dir := t.TempDir()
	cfg := config.Config{Secrets: dir}
	var asked []string
	allow := true
	var reports []string
	f := New(cfg, func(q approve.Question, then func(error)) (bool, error) {
		asked = append(asked, q.Text)
		if !allow {
			then(errors.New("denied"))
		} else {
			then(nil)
		}
		return true, nil
	}, nil)
	f.Report = func(turn, agent, user string) { reports = append(reports, turn+" "+agent) }
	f.client.Transport = server.Client().Transport

	do := func(method, path, body string) (Response, error) {
		return f.Do(context.Background(), Request{
			Secret: "github", Method: method, URL: server.URL + path, Body: []byte(body),
			Header: map[string]string{"Authorization": "stolen"},
		})
	}

	if _, err := do("GET", "/repos/me/notes", ""); !errors.Is(err, ErrSecret) {
		t.Fatalf("without the secret: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "github_token"), []byte("tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := f.Do(context.Background(), Request{Secret: "mail", URL: server.URL}); !errors.Is(err, ErrRequest) {
		t.Fatalf("unknown secret: %v", err)
	}
	if _, err := f.Do(context.Background(), Request{Secret: "github", URL: "https://evil.example/repos"}); !errors.Is(err, ErrRequest) {
		t.Fatalf("other host: %v", err)
	}
	if _, err := f.Do(context.Background(), Request{Secret: "github", URL: strings.Replace(server.URL, "https", "http", 1)}); !errors.Is(err, ErrRequest) {
		t.Fatalf("plain http: %v", err)
	}

	// Reads and ordinary changes to the user's own repositories go at once.
	resp, err := do("GET", "/repos/someone/else", "")
	if err != nil || resp.Status != 200 || string(resp.Body) != "ok" || resp.Header["Link"] == "" {
		t.Fatalf("get: %+v %v", resp, err)
	}
	for _, c := range [][3]string{
		{"POST", "/repos/me/notes/issues", `{"title":"x"}`},
		{"PATCH", "/repos/me/notes/issues/1", `{"state":"closed"}`},
		{"POST", "/graphql", `{"query":"query { viewer { login } }"}`},
		{"POST", "/user/repos", `{"name":"x","private":true}`},
		{"POST", "/repos/me/notes/pulls", `{"title":"x","head":"fix","base":"main"}`},
		{"POST", "/repos/me/notes/issues/1/comments", `{"body":"hi"}`},
		{"POST", "/repos/me/notes/git/blobs", `{"content":"x"}`},
		{"POST", "/graphql", `{"query":"query Q($n: String = \"mutation\") { viewer { login } } # mutation"}`},
	} {
		if _, err := do(c[0], c[1], c[2]); err != nil {
			t.Fatalf("%v: %v", c, err)
		}
	}
	if len(asked) != 0 {
		t.Fatalf("asked %q", asked)
	}

	// Everything else waits for the user.
	for _, c := range [][3]string{
		{"POST", "/repos/other/x/issues/1/comments", `{"body":"hi"}`},
		{"DELETE", "/repos/me/notes", ""},
		{"PUT", "/repos/me/notes/collaborators/bob", ""},
		{"PATCH", "/repos/me/notes/git/refs/heads/main", `{"sha":"abc","force":true}`},
		{"POST", "/graphql", `{"query":"mutation { addStar(input:{}) { clientMutationId } }"}`},
		{"POST", "/user/repos", `{"name":"x"}`},
		{"PUT", "/repos/me/notes/contents/README.md", `{"message":"x","content":"eA=="}`},
		{"POST", "/repos/me/notes/merges", `{"base":"main","head":"fix"}`},
		{"PUT", "/repos/me/notes/pulls/1/merge", ``},
		{"POST", "/repos/me/notes/git/refs", `{"ref":"refs/tags/v9","sha":"abc"}`},
		{"POST", "/repos/me/notes/transfer", `{"new_owner":"evil"}`},
		{"POST", "/repos/me/notes/releases", `{"tag_name":"v9"}`},
		{"POST", "/repos/me/notes/actions/workflows/ci.yml/dispatches", `{"ref":"main"}`},
		{"POST", "/graphql", `{"query":"query A { viewer { login } } mutation B { addStar(input:{}) { clientMutationId } }","operationName":"B"}`},
		{"POST", "/graphql", `{"query":"mutation { addStar(input:{}) { clientMutationId } }","Query":"{ viewer { login } }"}`},
		{"POST", "/user/repos", `{"name":"x","private":false,"Private":true}`},
	} {
		if resp, err := do(c[0], c[1], c[2]); err != nil || !resp.Waiting {
			t.Fatalf("%v: %+v %v", c, resp, err)
		}
	}
	if len(reports) != 16 || !strings.HasPrefix(reports[0], " [den: the user allowed GitHub: POST /repos/other/x/issues/1/comments, and den sent it. The service answered 200, quoted below") || !strings.Contains(reports[0], ">\nok\n</quoted ") {
		t.Fatalf("reports %q", reports)
	}
	if len(asked) != 16 || !strings.HasPrefix(asked[0], "GitHub: POST /repos/other/x/issues/1/comments\nWrites outside your own repositories.\n\n{\n  \"body\": \"hi\"\n}") {
		t.Fatalf("asked %q", asked)
	}

	allow = false
	if resp, err := do("POST", "/repos/other/x/issues", `{"title":"no"}`); err != nil || !resp.Waiting {
		t.Fatalf("denied: %+v %v", resp, err)
	}
	if got := reports[len(reports)-1]; got != " [den: GitHub: POST /repos/other/x/issues was not sent: denied.]" {
		t.Fatalf("denied report %q", got)
	}

	mu.Lock()
	for _, s := range seen {
		if !strings.HasSuffix(s, " Bearer tok") {
			t.Fatalf("server saw %q", s)
		}
		if strings.HasPrefix(s, "POST /repos/other/x/issues ") {
			t.Fatalf("the denied request reached the server")
		}
	}
	mu.Unlock()

	// A redirect to a host outside the service is not followed.
	resp, err = do("GET", "/away", "")
	if err != nil || resp.Status != http.StatusFound || resp.Header["Location"] != "https://elsewhere.example/x" {
		t.Fatalf("redirect: %+v %v", resp, err)
	}
}

func TestPathPattern(t *testing.T) {
	for path, want := range map[string]string{
		"/repos/me/notes/issues/12/comments":              "/repos/me/notes/issues/*/comments",
		"/api/deals/3f2b8c1e-9a7d-4e21-b5a0-1c2d3e4f5a6b": "/api/deals/*",
		"/api/topics/01M5WRZWF8915TWQHD3CRAKPE0":          "/api/topics/*",
		"/api/topics/01m5wrzwf8915twqhd3crakpe0":          "/api/topics/*",
		"/api/topics/release-notes-for-version-2x":        "/api/topics/release-notes-for-version-2x",
		"/repos/me/2024/issues/3":                         "/repos/me/2024/issues/*",
		"/api/topics/getting-started-guide":               "/api/topics/getting-started-guide",
		"/api/deals":                                      "/api/deals",
		"/repos/me/notes/git/refs/heads/v2":               "/repos/me/notes/git/refs/heads/v2",
	} {
		if got := pathPattern(path); got != want {
			t.Errorf("pathPattern(%s) = %s, want %s", path, got, want)
		}
	}
}
