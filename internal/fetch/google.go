package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/unsady/den/internal/letter"
)

// google reaches Gmail and Google Calendar with the account the user
// connected with den connect google.
var google = service{
	label: "Google",
	hosts: []string{"gmail.googleapis.com", "www.googleapis.com"},
	token: func(ctx context.Context, f *Fetcher) (string, error) {
		return f.google.Token(ctx)
	},
	authorize: func(r *http.Request, token string) {
		r.Header.Set("Authorization", "Bearer "+token)
	},
	review:  reviewGoogle,
	refuse:  refuseGoogle,
	preview: previewDraft,
	show:    showMail,
}

// reviewGoogle lets reads and changes to the user's own mail and calendar
// through, and asks before mail goes out, before other people get an
// invitation or an update, and before an event with other people on it
// changes or any event is deleted.
func reviewGoogle(ctx context.Context, f *Fetcher, s service, method string, u *url.URL, body []byte) string {
	if method == http.MethodGet || method == http.MethodHead {
		return ""
	}
	path := u.Path

	switch {
	case strings.HasSuffix(path, "/messages/send"):
		return "Sends an email."

	case strings.HasSuffix(path, "/drafts/send"):
		return "Sends a draft."

	case strings.HasPrefix(path, "/calendar/"):
		if method == http.MethodDelete {
			return "Deletes a calendar event."
		}
		q := u.Query()
		if update := q.Get("sendUpdates"); update != "" && update != "none" || q.Get("sendNotifications") == "true" {
			return "Sends other people an invitation or an update."
		}
		if strings.HasSuffix(path, "/quickAdd") || strings.HasSuffix(path, "/move") {
			return "Adds or moves a calendar event."
		}
		var attendees []json.RawMessage
		var status string
		if !field(body, "attendees", &attendees) || !field(body, "status", &status) {
			return "Changes a calendar event in a way den cannot read."
		}
		if len(attendees) > 0 {
			return "Changes an event that has other people on it."
		}
		if status == "cancelled" {
			return "Cancels a calendar event."
		}
		// A change to an event someone else is on reaches them too.
		if method != http.MethodPost && f.guests(ctx, s, u) {
			return "Changes an event that has other people on it."
		}
	}
	return ""
}

// guests says whether the event at u has other people on it, or den cannot
// tell.
func (f *Fetcher) guests(ctx context.Context, s service, u *url.URL) bool {
	token, err := f.secret(ctx, s)
	if err != nil {
		return true
	}
	r, err := http.NewRequestWithContext(context.WithValue(ctx, serviceKey{}, s), http.MethodGet, "https://"+u.Host+u.EscapedPath(), nil)
	if err != nil {
		return true
	}
	s.authorize(r, token)
	resp, err := f.client.Do(r)
	if err != nil {
		return true
	}
	defer resp.Body.Close()
	var event struct {
		Attendees []json.RawMessage `json:"attendees"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, MaxBody)).Decode(&event) != nil {
		return true
	}
	return len(event.Attendees) > 0
}

// refuseGoogle turns away what den cannot review: a batch request carries
// other requests inside it, such as one that sends mail.
func refuseGoogle(u *url.URL) string {
	if strings.HasPrefix(u.Path, "/batch") {
		return "den does not pass on batch requests; send each request on its own"
	}
	return ""
}

// previewDraft shows the draft a request sends, which its body names only
// by ID. Other requests need no preview.
func previewDraft(ctx context.Context, f *Fetcher, s service, u *url.URL, body []byte) (string, error) {
	if !strings.HasSuffix(u.Path, "/drafts/send") {
		return "", nil
	}
	var id string
	if !field(body, "id", &id) || id == "" {
		return "", errors.New("den sends a draft only when the body names it by id")
	}
	token, err := f.secret(ctx, s)
	if err != nil {
		return "", err
	}

	drafts := strings.TrimSuffix(u.Path, "/send")
	link := "https://" + u.Host + drafts + "/" + url.PathEscape(id) + "?format=raw"
	r, err := http.NewRequestWithContext(context.WithValue(ctx, serviceKey{}, s), http.MethodGet, link, nil)
	if err != nil {
		return "", err
	}
	s.authorize(r, token)
	resp, err := f.client.Do(r)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var got struct {
		Message struct {
			Raw string `json:"raw"`
		} `json:"message"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, MaxBody)).Decode(&got) != nil || got.Message.Raw == "" {
		return "", fmt.Errorf("den could not read draft %s to show it (status %d)", id, resp.StatusCode)
	}
	return showMail([]byte(`{"raw":"`+got.Message.Raw+`"}`)) + "\n\n" + digest(got.Message.Raw), nil
}

// digest names a draft's whole raw message in the preview, so a change past
// what the preview shows is still a change.
func digest(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("Draft %x", sum[:4])
}

// showMail shows an email that a request sends as its headers and text,
// instead of the base64 that Gmail takes. Other bodies show as they are.
func showMail(body []byte) string {
	raw := body
	var wrapped struct {
		Raw     string `json:"raw"`
		Message struct {
			Raw string `json:"raw"`
		} `json:"message"`
	}
	if json.Unmarshal(body, &wrapped) == nil {
		encoded := wrapped.Raw
		if encoded == "" {
			encoded = wrapped.Message.Raw
		}
		decoded, err := decodeBase64(encoded)
		if encoded == "" || err != nil {
			return show(body)
		}
		raw = decoded
	}

	if text := letter.Show(raw, nil); text != "" {
		return text
	}
	return show(body)
}

// decodeBase64 takes Gmail's raw message in either base64 alphabet, padded
// or not.
func decodeBase64(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	if strings.ContainsAny(s, "+/") {
		return base64.RawStdEncoding.DecodeString(s)
	}
	return base64.RawURLEncoding.DecodeString(s)
}
