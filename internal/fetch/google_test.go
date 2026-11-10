package fetch

import (
	"context"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
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
	}

	// No account is connected, so a draft is not fetched to show it.
	f := &Fetcher{}
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
