package letter

import (
	"strings"
	"testing"
)

func TestShowsWhatTheRecipientReads(t *testing.T) {
	msg := "From: me@example.com\r\nReply-To: thief@example.net\r\nTo: anya@example.com\r\nSubject: Invoice\r\n" +
		"Content-Type: multipart/alternative; boundary=b\r\n\r\n" +
		"--b\r\nContent-Type: text/plain\r\n\r\nSee you at 7\r\n" +
		"--b\r\nContent-Type: text/html\r\n\r\n<html><head><style>p{}</style></head><p>Pay now at <a href=x>this link</a></p>\r\n--b--\r\n"
	got := Show([]byte(msg), []string{"anya@example.com"})
	for _, want := range []string{"Reply-To: thief@example.net", "See you at 7", "reads differently:\nPay now at this link"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Bcc") || strings.Contains(got, "p{}") {
		t.Errorf("shown:\n%s", got)
	}

	// An HTML version that says the same adds nothing.
	same := strings.Replace(msg, "<p>Pay now at <a href=x>this link</a></p>", "<p>See you at 7</p>", 1)
	if got := Show([]byte(same), nil); strings.Contains(got, "HTML") {
		t.Errorf("same HTML shown:\n%s", got)
	}
}
