package main

import (
	"strings"
	"testing"
)

func TestSentRemindsOfWidget(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 16))

	if got := sent(png, false); !strings.Contains(got, "--widget") {
		t.Errorf("a picture without data: %q", got)
	}
	if got := sent(png, true); got != "sent" {
		t.Errorf("a picture with data: %q", got)
	}
	if got := sent([]byte("%PDF-1.7"), false); got != "sent" {
		t.Errorf("a document: %q", got)
	}
}
