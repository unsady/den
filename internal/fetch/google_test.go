package fetch

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsady/den/internal/approve"
	"github.com/unsady/den/internal/config"
	googleauth "github.com/unsady/den/internal/google"
)

func TestReviewGoogle(t *testing.T) {
	cases := []struct {
		method, link, body string
		ask                bool
	}{
		{"GET", "https://gmail.googleapis.com/gmail/v1/users/me/messages?q=is:unread", "", false},
		{"POST", "https://gmail.googleapis.com/gmail/v1/users/me/messages/abc/modify", `{"removeLabelIds":["UNREAD"]}`, false},
		{"POST", "https://gmail.googleapis.com/gmail/v1/users/me/messages/abc/trash", "", false},
		{"POST", "https://gmail.googleapis.com/gmail/v1/users/me/drafts", `{"message":{"raw":"eA"}}`, false},
		{"POST", "https://gmail.googleapis.com/gmail/v1/users/me/messages/send", `{"raw":"eA"}`, true},
		{"POST", "https://www.googleapis.com/upload/gmail/v1/users/me/messages/send?uploadType=media", "From: a", true},
		{"POST", "https://gmail.googleapis.com/gmail/v1/users/me/drafts/send", `{"id":"r1"}`, true},
		{"POST", "https://www.googleapis.com/calendar/v3/calendars/primary/events", `{"summary":"Gym"}`, false},
		{"POST", "https://www.googleapis.com/calendar/v3/calendars/primary/events", `{"summary":"Call","attendees":[{"email":"x@y.z"}]}`, true},
		{"PATCH", "https://www.googleapis.com/calendar/v3/calendars/primary/events/e1?sendUpdates=all", `{}`, true},
		{"DELETE", "https://www.googleapis.com/calendar/v3/calendars/primary/events/e1", "", true},
		{"PATCH", "https://www.googleapis.com/calendar/v3/calendars/primary/events/e1", `{"status":"cancelled"}`, true},
		{"PATCH", "https://www.googleapis.com/calendar/v3/calendars/primary/events/e1?sendNotifications=true", `{"summary":"x"}`, true},
		{"POST", "https://www.googleapis.com/calendar/v3/calendars/primary/events", `{"attendees":[{"email":"x@y.z"}],"Attendees":[]}`, true},
		{"POST", "https://www.googleapis.com/calendar/v3/calendars/primary/events/quickAdd?text=Lunch", ``, true},
		{"POST", "https://gmail.googleapis.com/gmail/v1/users/me/threads/t1/untrash", "", false},
		{"POST", "https://gmail.googleapis.com/gmail/v1/users/me/messages/batchModify", `{"ids":["a"],"addLabelIds":["STARRED"]}`, false},
		{"PUT", "https://gmail.googleapis.com/gmail/v1/users/me/drafts/d1", `{"message":{"raw":"eA"}}`, false},
		{"POST", "https://www.googleapis.com/upload/gmail/v1/users/me/drafts?uploadType=media", "To: a", false},
		{"POST", "https://gmail.googleapis.com/gmail/v1/users/me/labels", `{"name":"Bills"}`, false},

		// What den does not know, and deletes, ask.
		{"DELETE", "https://gmail.googleapis.com/gmail/v1/users/me/labels/l1", "", true},
		{"DELETE", "https://gmail.googleapis.com/gmail/v1/users/me/drafts/d1", "", true},
		{"POST", "https://gmail.googleapis.com/gmail/v1/users/me/messages/import", `{"raw":"eA"}`, true},
		{"POST", "https://gmail.googleapis.com/gmail/v1/users/me/watch", `{"topicName":"projects/x/topics/y"}`, true},
		{"POST", "https://gmail.googleapis.com/gmail/v1/users/someone@example.com/drafts", `{}`, true},
		{"POST", "https://www.googleapis.com/calendar/v3/calendars/primary/events/watch", `{"address":"https://example.com"}`, true},
		{"POST", "https://www.googleapis.com/calendar/v3/calendars/primary/events/import", `{}`, true},
		{"POST", "https://www.googleapis.com/calendar/v3/calendars/primary/events/e1/move?destination=team", ``, true},
		{"POST", "https://www.googleapis.com/calendar/v3/channels/stop", `{}`, true},
		{"POST", "https://www.googleapis.com/oauth2/v2/anything", `{}`, true},

		// Another calendar may be shared, so it asks even for an event alone.
		{"POST", "https://www.googleapis.com/calendar/v3/calendars/team@group.calendar.google.com/events", `{"summary":"Gym"}`, true},

		// den cannot read the event to see its guests, so it asks.
		{"PATCH", "https://www.googleapis.com/calendar/v3/calendars/primary/events/e1", `{"summary":"Gym"}`, true},
	}

	// No account is connected, so a draft is not fetched to show it.
	f := &Fetcher{client: http.DefaultClient}
	f.google = nil
	s := google
	s.token = func(context.Context, *Fetcher) (string, error) { return "", ErrSecret }

	for _, c := range cases {
		u, _ := url.Parse(c.link)
		why := reviewGoogle(context.Background(), f, s, c.method, u, []byte(c.body))
		if (why != "") != c.ask {
			t.Errorf("%s %s %s: %q", c.method, c.link, c.body, why)
		}
	}
}

func TestShowMail(t *testing.T) {
	message := "From: me@example.com\r\nTo: =?UTF-8?B?Wm/Dqw==?= <zoe@example.com>\r\nSubject: Hi\r\n" +
		"Content-Type: multipart/mixed; boundary=b\r\n\r\n" +
		"--b\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		base64.StdEncoding.EncodeToString([]byte("Grüße, see you at 6")) + "\r\n" +
		"--b\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"plan.pdf\"\r\n\r\nxxx\r\n--b--\r\n"
	body := `{"raw":"` + base64.URLEncoding.EncodeToString([]byte(message)) + `"}`

	got := showMail([]byte(body))
	for _, want := range []string{"To: Zoë <zoe@example.com>", "Subject: Hi", "Grüße, see you at 6", "📎 plan.pdf"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	if got := showMail([]byte(`{"summary":"Gym"}`)); !strings.Contains(got, `"summary": "Gym"`) {
		t.Errorf("plain JSON: %s", got)
	}
}

func TestDraftChangedAfterTheQuestion(t *testing.T) {
	raw := base64.URLEncoding.EncodeToString([]byte("To: anya@example.com\r\nSubject: Hi\r\n\r\nSee you\r\n"))
	var sent []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"message":{"raw":"`+raw+`"}}`)
			return
		}
		sent = append(sent, r.URL.Path)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	g := google
	g.hosts = []string{"127.0.0.1"}
	g.token = func(context.Context, *Fetcher) (string, error) { return "tok", nil }
	services["google"] = g
	defer func() { services["google"] = google }()

	var asked []string
	var then func(error)
	var reports []string
	f := New(config.Config{}, func(q approve.Question, answer func(error)) (bool, error) {
		asked = append(asked, q.Text)
		then = answer
		return true, nil
	}, googleauth.New(config.Config{Secrets: t.TempDir()}))
	f.client.Transport = server.Client().Transport
	f.Report = func(session, agent, user string) { reports = append(reports, agent) }
	f.Known = func(session string) bool { return session == "s1" }

	send := func() {
		resp, err := f.Do(context.Background(), Request{Session: "s1", Secret: "google", Method: "POST", URL: server.URL + "/gmail/v1/users/me/drafts/send", Body: []byte(`{"id":"r1"}`)})
		if err != nil || !resp.Waiting {
			t.Fatalf("%+v %v", resp, err)
		}
	}

	// The user sees the draft, and den sends it as it was.
	send()
	if !strings.Contains(asked[0], "See you") {
		t.Fatalf("asked %q", asked)
	}
	then(nil)
	if len(sent) != 1 {
		t.Fatalf("sent %q, reports %q", sent, reports)
	}

	// The agent changes the draft while the question waits: nothing goes.
	send()
	raw = base64.URLEncoding.EncodeToString([]byte("To: boss@example.com\r\nSubject: Hi\r\n\r\nSomething else\r\n"))
	then(nil)
	if len(sent) != 1 || !strings.Contains(reports[1], "changed after the user saw it") {
		t.Fatalf("sent %q, reports %q", sent, reports)
	}

	// Batches and method overrides are turned away.
	if _, err := f.Do(context.Background(), Request{Session: "s1", Secret: "google", Method: "POST", URL: server.URL + "/batch/gmail/v1"}); !errors.Is(err, ErrRequest) {
		t.Fatalf("batch: %v", err)
	}
	if _, err := f.Do(context.Background(), Request{Session: "s1", Secret: "google", Method: "POST", URL: server.URL + "/calendar/v3/calendars/primary/events", Header: map[string]string{"X-HTTP-Method-Override": "DELETE"}}); !errors.Is(err, ErrRequest) {
		t.Fatalf("override: %v", err)
	}
}

func TestReadOnly(t *testing.T) {
	for doc, want := range map[string]bool{
		`{ viewer { login } }`:                                   true,
		`query { repository(name: "mutation") { id } }`:          true,
		"query Q { a } # mutation":                               true,
		`fragment F on User { login } query { viewer { ...F } }`: true,
		`mutation { addStar(input: {}) { clientMutationId } }`:   false,
		`query A { a } mutation B { b }`:                         false,
		`subscription { x }`:                                     false,
		`query { a } """ unterminated`:                           false,
	} {
		if got := readOnly(doc); got != want {
			t.Errorf("readOnly(%s) = %v", doc, got)
		}
	}

	if _, err := object([]byte(`{"query":"a","Query":"b"}`)); err == nil {
		t.Error("a key that comes twice was taken")
	}
	if _, err := object([]byte(`{"query":"a"} {}`)); err == nil {
		t.Error("a second object was taken")
	}
}

func TestGoogleSource(t *testing.T) {
	cases := map[string]string{
		"https://gmail.googleapis.com/gmail/v1/users/me/messages/send":    "Gmail",
		"https://www.googleapis.com/calendar/v3/calendars/primary/events": "Calendar",
		"https://www.googleapis.com/drive/v3/files":                       "Drive",
		"https://sheets.googleapis.com/v4/spreadsheets/s1":                "Sheets",
		"https://www.googleapis.com/oauth2/v2/userinfo":                   "Google",
	}
	for link, want := range cases {
		u, _ := url.Parse(link)
		if got := googleSource(u); got != want {
			t.Errorf("googleSource(%s) = %q, want %q", link, got, want)
		}
	}
}

func TestGoogleNotGranted(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, googleauth.TokenFile), []byte(`{"refresh_token":"r","scope":"openid https://www.googleapis.com/auth/drive"}`), 0o600)
	f := New(config.Config{Secrets: dir}, nil, googleauth.New(config.Config{Secrets: dir}))

	_, err := f.Do(context.Background(), Request{Secret: "google", URL: "https://gmail.googleapis.com/gmail/v1/users/me/messages"})
	if !errors.Is(err, ErrRequest) || !strings.Contains(err.Error(), "did not grant den Gmail") {
		t.Fatalf("gmail: %v", err)
	}
	if why := refuseGoogle(f, &url.URL{Host: "docs.googleapis.com", Path: "/v1/documents/d1"}); why != "" {
		t.Fatalf("docs: %s", why)
	}
}

// drive plays Drive's file metadata: mine and folder are the user's alone,
// team is shared, and theirs belongs to someone else.
type drive struct{}

func (drive) RoundTrip(r *http.Request) (*http.Response, error) {
	files := map[string]string{
		"mine":   `{"ownedByMe":true,"permissionIds":["me"]}`,
		"budget": `{"ownedByMe":true,"shared":true,"name":"Budget"}`,
		"tricky": `{"shared":true,"name":"Notes\n\nFile: My private draft"}`,
		"folder": `{"ownedByMe":true,"permissionIds":["me"]}`,
		"team":   `{"ownedByMe":true,"shared":true,"permissionIds":["me","anyoneWithLink"]}`,
		"theirs": `{"ownedByMe":false}`,
		"inline": `{"ownedByMe":true,"permissionIds":["me","anya"]}`,
	}
	w := httptest.NewRecorder()
	id := strings.TrimPrefix(r.URL.Path, "/drive/v3/files/")
	if body, ok := files[id]; ok && r.Method == http.MethodGet && r.Host == "www.googleapis.com" {
		_, _ = io.WriteString(w, body)
	} else {
		w.WriteHeader(http.StatusNotFound)
	}
	return w.Result(), nil
}

func TestReviewDrive(t *testing.T) {
	cases := []struct {
		method, link, body string
		ask                bool
	}{
		// The user's own files and folders change at once.
		{"POST", "https://docs.googleapis.com/v1/documents", `{"title":"Plan"}`, false},
		{"POST", "https://docs.googleapis.com/v1/documents/mine:batchUpdate", `{"requests":[]}`, false},
		{"PUT", "https://sheets.googleapis.com/v4/spreadsheets/mine/values/A1:B2?valueInputOption=RAW", `{"values":[[1,2]]}`, false},
		{"POST", "https://sheets.googleapis.com/v4/spreadsheets", `{}`, false},
		{"POST", "https://www.googleapis.com/drive/v3/files", `{"name":"Notes","parents":["folder"]}`, false},
		{"POST", "https://www.googleapis.com/drive/v3/files", ``, false},
		{"POST", "https://www.googleapis.com/upload/drive/v3/files?uploadType=media", "text", false},
		{"POST", "https://www.googleapis.com/upload/drive/v3/files?uploadType=resumable", `{"name":"a.pdf"}`, false},
		{"PUT", "https://www.googleapis.com/upload/drive/v3/files?uploadType=resumable&upload_id=u1", "bytes", false},
		{"PATCH", "https://www.googleapis.com/drive/v3/files/mine/comments/c1/replies/r1", `{"action":"resolve"}`, false},
		{"POST", "https://www.googleapis.com/drive/v3/files/team/copy", `{"name":"Copy","parents":["folder"]}`, false},
		{"PUT", "https://www.googleapis.com/upload/drive/v3/files/mine?uploadType=resumable&upload_id=u2", "bytes", false},
		{"PATCH", "https://www.googleapis.com/drive/v3/files/mine?addParents=folder", `{"trashed":true}`, false},
		{"PATCH", "https://www.googleapis.com/upload/drive/v3/files/mine?uploadType=media", "text", false},
		{"POST", "https://www.googleapis.com/drive/v3/files/mine/comments?fields=id", `{"content":"Typo here"}`, false},

		// What other people can open waits for the user.
		{"POST", "https://docs.googleapis.com/v1/documents/team:batchUpdate", `{"requests":[]}`, true},
		{"POST", "https://sheets.googleapis.com/v4/spreadsheets/theirs/values/A:B:append", `{}`, true},
		{"PATCH", "https://www.googleapis.com/drive/v3/files/inline", `{"name":"x"}`, true},
		{"PATCH", "https://www.googleapis.com/drive/v3/files/mine?addParents=folder,team", `{}`, true},
		{"POST", "https://www.googleapis.com/drive/v3/files", `{"name":"Notes","parents":["team"]}`, true},
		{"POST", "https://www.googleapis.com/drive/v3/files/mine/copy", `{"parents":["theirs"]}`, true},
		{"POST", "https://www.googleapis.com/drive/v3/files/team/comments", `{"content":"Typo"}`, true},
		{"POST", "https://www.googleapis.com/drive/v3/files/unknown/comments", `{"content":"Typo"}`, true},

		// So do deletes, mentions, and what den does not know.
		{"DELETE", "https://www.googleapis.com/drive/v3/files/mine", "", true},
		{"DELETE", "https://www.googleapis.com/drive/v3/files/trash", "", true},
		{"POST", "https://www.googleapis.com/drive/v3/files/mine/comments/c1/replies", `{"content":"+anya@example.com look"}`, true},
		{"POST", "https://www.googleapis.com/drive/v3/files/mine/permissions", `{"role":"writer","type":"anyone"}`, true},
		{"PATCH", "https://www.googleapis.com/drive/v3/files/mine/revisions/head", `{"published":true}`, true},
		{"POST", "https://www.googleapis.com/drive/v3/files/mine/accessproposals/p1:resolve", `{"action":"ACCEPT"}`, true},
		{"POST", "https://www.googleapis.com/drive/v3/files/mine/watch", `{"address":"https://example.com"}`, true},
		{"POST", "https://www.googleapis.com/upload/drive/v3/files?uploadType=multipart", "--b", true},
		{"POST", "https://www.googleapis.com/drive/v3/files", `{"parents":"folder"}`, true},
		{"POST", "https://sheets.googleapis.com/v4/spreadsheets/mine/sheets/0:copyTo", `{"destinationSpreadsheetId":"team"}`, true},
		{"POST", "https://www.googleapis.com/drive/v3/drives", `{"name":"Team"}`, true},

		// Ways around the checks ask too.
		{"POST", "https://www.googleapis.com/drive/v3/files/team/copy", `{"name":"Copy"}`, true},
		{"POST", "https://www.googleapis.com/drive/v3/files/mine/copy", ``, true},
		{"PATCH", "https://www.googleapis.com/drive/v2/files/mine", `{"parents":[{"id":"team"}]}`, true},
		{"PUT", "https://www.googleapis.com/drive/v3/files/team?upload_id=x", `{}`, true},
		{"PUT", "https://www.googleapis.com/upload/drive/v3/files/team?uploadType=resumable&upload_id=u2", "bytes", true},
		{"POST", "https://www.googleapis.com/drive/v3/files/mine/comments", `{"content":"+anya\u0040example.com look"}`, true},
		{"POST", "https://www.googleapis.com/upload/drive/v3/files?uploadType=media&uploadType=multipart", "--b", true},
		{"PATCH", "https://www.googleapis.com/drive/v3/files/mine?addParents=folder&addParents=team", `{}`, true},
		{"POST", "https://docs.googleapis.com/v2/documents/mine:batchUpdate", `{}`, true},
		{"POST", "https://gmail.googleapis.com/gmail/v2/users/me/drafts", `{}`, true},
		{"PATCH", "https://www.googleapis.com/drive/v3/files/mine?add_parents=team", `{}`, true},
		{"PATCH", "https://www.googleapis.com/drive/v3/files/mine?addParents=folder;addParents=team", `{}`, true},
		{"PATCH", "https://www.googleapis.com/drive/v3/files/mine?removeParents=team", `{}`, true},
		{"PATCH", "https://www.googleapis.com/calendar/v3/calendars/primary/events/e1?send_updates=all", `{}`, true},
		{"POST", "https://www.googleapis.com/drive/v3/files/mine/comments", `{"Content":"+anya@example.com look"}`, true},
	}

	f := &Fetcher{client: &http.Client{Transport: drive{}}}
	s := google
	s.token = func(context.Context, *Fetcher) (string, error) { return "tok", nil }

	for _, c := range cases {
		u, _ := url.Parse(c.link)
		why := reviewGoogle(context.Background(), f, s, c.method, u, []byte(c.body))
		if (why != "") != c.ask {
			t.Errorf("%s %s %s: %q", c.method, c.link, c.body, why)
		}
	}
}

func TestOwnCalendar(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, googleauth.TokenFile), []byte(`{"refresh_token":"r","email":"me@example.com"}`), 0o600)
	f := &Fetcher{google: googleauth.New(config.Config{Secrets: dir})}

	for id, want := range map[string]bool{"primary": true, "Me@Example.com": true, "team@group.calendar.google.com": false, "": false} {
		if got := f.own(id); got != want {
			t.Errorf("own(%q) = %v", id, got)
		}
	}
}

func TestOfferFileEdits(t *testing.T) {
	cases := []struct {
		method, link string
		rule         string
	}{
		{"POST", "https://docs.googleapis.com/v1/documents/budget:batchUpdate", "fetch google edit budget"},
		{"PUT", "https://sheets.googleapis.com/v4/spreadsheets/team/values/A1:B2?valueInputOption=RAW", "fetch google edit team"},

		{"PATCH", "https://www.googleapis.com/upload/drive/v3/files/team?uploadType=media", "fetch google edit team"},
		{"PUT", "https://www.googleapis.com/upload/drive/v3/files/team?uploadType=resumable&upload_id=u1", "fetch google edit team"},

		// Metadata, moves, sharing, comments, and copies ask each time.
		{"PATCH", "https://www.googleapis.com/drive/v3/files/team", ""},
		{"PATCH", "https://www.googleapis.com/upload/drive/v3/files/team?uploadType=media&addParents=x", ""},
		{"POST", "https://www.googleapis.com/drive/v3/files/team/permissions", ""},
		{"POST", "https://www.googleapis.com/drive/v3/files/team/comments", ""},
		{"POST", "https://sheets.googleapis.com/v4/spreadsheets/team/sheets/0:copyTo", ""},
		{"POST", "https://docs.googleapis.com/v2/documents/team:batchUpdate", ""},
		{"POST", "https://docs.googleapis.com/v1/documents/team:batchUpdate?a=1&a=2", ""},
		{"DELETE", "https://sheets.googleapis.com/v4/spreadsheets/team", ""},
		{"PATCH", "https://www.googleapis.com/upload/drive/v3/files/team?uploadType=resumable", ""},
		{"PATCH", "https://www.googleapis.com/upload/drive/v3/files/team?uploadType=multipart", ""},
		{"PATCH", "https://www.googleapis.com/upload/drive/v3/files/team?uploadType=media&removeParents=x", ""},
		{"PATCH", "https://www.googleapis.com/upload/drive/v3/files/team?uploadType=media&upload_type=multipart", ""},
	}

	f := &Fetcher{client: &http.Client{Transport: drive{}}}
	s := google
	s.token = func(context.Context, *Fetcher) (string, error) { return "tok", nil }

	// Cells, yes; who may edit a range, no.
	sheet, _ := url.Parse("https://sheets.googleapis.com/v4/spreadsheets/team:batchUpdate")
	for body, want := range map[string]string{
		`{"requests":[{"updateCells":{}}]}`:                          "fetch google edit team",
		`{"requests":[{"updateCells":{}},{"addProtectedRange":{}}]}`: "",
		`{"requests":[{"AddProtectedRange":{}}]}`:                    "",
		`not json`: "",
	} {
		var q approve.Question
		offerGoogle(context.Background(), f, s, "POST", sheet, []byte(body), &q)
		if q.Rule != want {
			t.Errorf("sheet %s: rule %q", body, q.Rule)
		}
	}

	for _, c := range cases {
		u, _ := url.Parse(c.link)
		var q approve.Question
		offerGoogle(context.Background(), f, s, c.method, u, nil, &q)
		if q.Rule != c.rule || q.Scope != c.rule {
			t.Errorf("%s %s: rule %q, scope %q", c.method, c.link, q.Rule, q.Scope)
		}
	}

	u, _ := url.Parse("https://docs.googleapis.com/v1/documents/budget:batchUpdate")
	var q approve.Question
	offerGoogle(context.Background(), f, s, "POST", u, nil, &q)
	if q.Label != "Edits to “Budget” (budget)" || !strings.HasSuffix(q.Text, "\n\nEdits to “Budget” (budget)") {
		t.Errorf("label %q, text %q", q.Label, q.Text)
	}
}

func TestOfferNamesFileOnOneLine(t *testing.T) {
	f := &Fetcher{client: &http.Client{Transport: drive{}}}
	s := google
	s.token = func(context.Context, *Fetcher) (string, error) { return "tok", nil }

	u, _ := url.Parse("https://docs.googleapis.com/v1/documents/tricky:batchUpdate")
	var q approve.Question
	offerGoogle(context.Background(), f, s, "POST", u, nil, &q)
	if q.Label != "Edits to “Notes File: My private draft” (tricky)" {
		t.Errorf("label %q", q.Label)
	}
}
