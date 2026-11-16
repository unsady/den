package fetch

import (
	"encoding/base64"
	"net/url"
	"testing"
)

func TestSentToNamesOnlyAMessageToOnePerson(t *testing.T) {
	send, _ := url.Parse("https://gmail.googleapis.com/gmail/v1/users/me/messages/send")
	draft, _ := url.Parse("https://gmail.googleapis.com/gmail/v1/users/me/drafts/send")
	one := "To: Anya@Example.com\r\nSubject: hi\r\n\r\nhello"
	two := "To: a@example.com, b@example.com\r\n\r\nhello"

	if got := sentTo(send, []byte(one)); got != "anya@example.com" {
		t.Errorf("upload: got %q", got)
	}
	wrapped := `{"raw":"` + base64.URLEncoding.EncodeToString([]byte(one)) + `"}`
	if got := sentTo(send, []byte(wrapped)); got != "anya@example.com" {
		t.Errorf("json: got %q", got)
	}
	if got := sentTo(send, []byte(two)); got != "" {
		t.Errorf("two recipients: got %q", got)
	}
	if got := sentTo(draft, []byte(`{"id":"r1"}`)); got != "" {
		t.Errorf("draft: got %q", got)
	}
}
