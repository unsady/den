package fetch

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/unsady/den/internal/approve"
	"github.com/unsady/den/internal/config"
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
	}, nil)
	f.client.Transport = server.Client().Transport
	f.Report = func(turn, agent, user string) { reports = append(reports, agent) }

	send := func() {
		resp, err := f.Do(context.Background(), Request{Secret: "google", Method: "POST", URL: server.URL + "/gmail/v1/users/me/drafts/send", Body: []byte(`{"id":"r1"}`)})
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
	if _, err := f.Do(context.Background(), Request{Secret: "google", Method: "POST", URL: server.URL + "/batch/gmail/v1"}); !errors.Is(err, ErrRequest) {
		t.Fatalf("batch: %v", err)
	}
	if _, err := f.Do(context.Background(), Request{Secret: "google", Method: "POST", URL: server.URL + "/calendar/v3/calendars/primary/events", Header: map[string]string{"X-HTTP-Method-Override": "DELETE"}}); !errors.Is(err, ErrRequest) {
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
