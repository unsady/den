package fetch

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
	review: reviewGoogle,
	show:   showMail,
}

// reviewGoogle lets reads and changes to the user's own mail and calendar
// through, and asks before mail goes out, before other people get an
// invitation or an update, and before an event is deleted.
func reviewGoogle(ctx context.Context, f *Fetcher, s service, method string, u *url.URL, body []byte) string {
	if method == http.MethodGet || method == http.MethodHead {
		return ""
	}
	path := u.Path

	switch {
	case strings.HasSuffix(path, "/messages/send"):
		return "Sends an email."

	case strings.HasSuffix(path, "/drafts/send"):
		return "Sends a draft." + f.draft(ctx, s, u, body)

	case strings.HasPrefix(path, "/calendar/"):
		if method == http.MethodDelete {
			return "Deletes a calendar event."
		}
		if update := u.Query().Get("sendUpdates"); update == "all" || update == "externalOnly" {
			return "Sends other people an invitation or an update."
		}
		var event struct {
			Attendees []json.RawMessage `json:"attendees"`
		}
		if json.Unmarshal(body, &event) == nil && len(event.Attendees) > 0 {
			return "Changes an event that has other people on it."
		}
	}
	return ""
}

// draft shows the user the draft that is about to go out.
func (f *Fetcher) draft(ctx context.Context, s service, u *url.URL, body []byte) string {
	var req struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(body, &req) != nil || req.ID == "" {
		return ""
	}
	token, err := f.secret(ctx, s)
	if err != nil {
		return ""
	}

	drafts := strings.TrimSuffix(u.Path, "/send")
	link := "https://" + u.Host + drafts + "/" + url.PathEscape(req.ID) + "?format=raw"
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return ""
	}
	s.authorize(r, token)

	resp, err := f.client.Do(r)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	var got struct {
		Message struct {
			Raw string `json:"raw"`
		} `json:"message"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, MaxBody)).Decode(&got) != nil || got.Message.Raw == "" {
		return ""
	}
	return "\n\n" + showMail([]byte(`{"raw":"`+got.Message.Raw+`"}`))
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
