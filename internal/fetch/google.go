package fetch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/unsady/den/internal/letter"
)

// google reaches Gmail, Google Calendar, Drive, Docs, and Sheets with the
// account the user connected with den connect google.
var google = service{
	label:  "Google",
	source: googleSource,
	hosts:  []string{"gmail.googleapis.com", "www.googleapis.com", "docs.googleapis.com", "sheets.googleapis.com"},
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
	// A draft's recipients are not in the request, so a draft always asks.
	recipient: sentTo,
}

// reviewGoogle lets reads and changes to the user's own mail, calendar,
// and files through, and asks before mail goes out, before other people get
// an invitation or an update, before an event with other people on it
// changes or any event is deleted, before a file is shared or deleted for
// good, and before a comment mentions someone.
func reviewGoogle(ctx context.Context, f *Fetcher, s service, method string, u *url.URL, body []byte) string {
	if method == http.MethodGet || method == http.MethodHead {
		return ""
	}
	path := u.Path

	if api(u) == "Drive" {
		return reviewDrive(ctx, f, s, method, u, body)
	}

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

// reviewDrive lets through the changes den knows to stay with the user:
// edits to files and comments no one else can open, and new files and
// copies in folders no one else can open. Every other change waits for the
// user, so a kind of request den does not know, such as publishing a doc to
// the web, asks too.
func reviewDrive(ctx context.Context, f *Fetcher, s service, method string, u *url.URL, body []byte) string {
	if method == http.MethodDelete {
		return "Deletes for good."
	}
	shared := func(ids ...string) bool {
		for _, id := range ids {
			if f.shared(ctx, s, id) {
				return true
			}
		}
		return false
	}
	q := u.Query()
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if parts[0] == "upload" {
		parts = parts[1:]
	}
	// A doc's or a sheet's id comes before a colon, as in d1:batchUpdate.
	id := func(i int) string {
		if len(parts) <= i {
			return ""
		}
		return strings.SplitN(parts[i], ":", 2)[0]
	}

	switch u.Hostname() {
	case "docs.googleapis.com":
		// /v1/documents, /v1/documents/<id>:batchUpdate
		if len(parts) == 2 && parts[1] == "documents" {
			return ""
		}
		if len(parts) == 3 && strings.HasSuffix(parts[2], ":batchUpdate") {
			if shared(id(2)) {
				return "Changes a doc other people can open."
			}
			return ""
		}
		return "Makes a change in Docs den does not know."

	case "sheets.googleapis.com":
		// /v4/spreadsheets, /v4/spreadsheets/<id>..., but a sheet copied to
		// another spreadsheet changes that one.
		if len(parts) == 2 && parts[1] == "spreadsheets" {
			return ""
		}
		if len(parts) >= 3 && parts[1] == "spreadsheets" && !strings.HasSuffix(u.Path, ":copyTo") {
			if shared(id(2)) {
				return "Changes a spreadsheet other people can open."
			}
			return ""
		}
		return "Makes a change in Sheets den does not know."
	}

	// /drive/v3/files..., with /upload before it for a file's content.
	if len(parts) < 3 || parts[0] != "drive" || parts[2] != "files" {
		return "Makes a change in Drive den does not know."
	}
	var parents []string
	readParents := func() bool {
		return len(bytes.TrimSpace(body)) == 0 || field(body, "parents", &parents)
	}
	switch {
	// A new file: its metadata names its folder, and a plain upload
	// goes to the top of My Drive. A multipart upload hides the metadata.
	case len(parts) == 3 && method == http.MethodPost:
		switch q.Get("uploadType") {
		case "media":
			return ""
		case "multipart":
			return "Uploads a file where den cannot read which folder it goes to; upload with uploadType=media or resumable."
		}
		if !readParents() {
			return "Creates a file where den cannot read which folder it goes to."
		}
		if shared(parents...) {
			return "Puts a new file in a folder other people can open."
		}
		return ""

	// The rest of a resumable upload, whose start den looked at.
	case method == http.MethodPut && q.Get("upload_id") != "":
		return ""

	// A copy is new, and only its folder matters.
	case len(parts) == 5 && parts[4] == "copy" && method == http.MethodPost:
		if !readParents() {
			return "Copies a file where den cannot read which folder it goes to."
		}
		if shared(parents...) {
			return "Puts a copy in a folder other people can open."
		}
		return ""

	// A file's metadata or content, and the folders it moves to.
	case len(parts) == 4 && method == http.MethodPatch:
		if shared(id(3)) {
			return "Changes a file other people can open."
		}
		if shared(strings.Split(q.Get("addParents"), ",")...) {
			return "Moves a file to a folder other people can open."
		}
		return ""

	// Comments and replies; a mention mails the person it names.
	case len(parts) >= 5 && parts[4] == "comments" && (method == http.MethodPost || method == http.MethodPatch):
		if shared(id(3)) {
			return "Comments on a file other people can open."
		}
		if bytes.Contains(body, []byte("@")) {
			return "Adds a comment that may mention someone, who then gets an email."
		}
		return ""
	}
	return "Makes a change in Drive den does not know."
}

// shared says whether other people can open the file or folder id: it is
// shared, sits in a shared drive, or belongs to someone else. It says so
// too when den cannot tell.
func (f *Fetcher) shared(ctx context.Context, s service, id string) bool {
	if id == "" {
		return false
	}
	var file struct {
		Shared      bool     `json:"shared"`
		Drive       string   `json:"driveId"`
		Mine        bool     `json:"ownedByMe"`
		Permissions []string `json:"permissionIds"`
	}
	link := "https://www.googleapis.com/drive/v3/files/" + url.PathEscape(id) + "?supportsAllDrives=true&fields=shared,driveId,ownedByMe,permissionIds"
	if !f.read(ctx, s, link, &file) {
		return true
	}
	return file.Shared || file.Drive != "" || !file.Mine || len(file.Permissions) > 1
}

// guests says whether the event at u has other people on it, or den cannot
// tell.
func (f *Fetcher) guests(ctx context.Context, s service, u *url.URL) bool {
	var event struct {
		Attendees []json.RawMessage `json:"attendees"`
	}
	if !f.read(ctx, s, "https://"+u.Host+u.EscapedPath(), &event) {
		return true
	}
	return len(event.Attendees) > 0
}

// read gets link with the service's secret into v, and says whether it
// could.
func (f *Fetcher) read(ctx context.Context, s service, link string, v any) bool {
	token, err := f.secret(ctx, s)
	if err != nil {
		return false
	}
	r, err := http.NewRequestWithContext(context.WithValue(ctx, serviceKey{}, s), http.MethodGet, link, nil)
	if err != nil {
		return false
	}
	s.authorize(r, token)
	resp, err := f.client.Do(r)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(resp.Body, MaxBody)).Decode(v) == nil
}

// googleSource names the Google API a request goes to, such as Gmail for
// /gmail/v1/users/me/messages/send.
func googleSource(u *url.URL) string {
	switch u.Hostname() {
	case "docs.googleapis.com":
		return "Docs"
	case "sheets.googleapis.com":
		return "Sheets"
	}
	if name := api(u); name != "" {
		return name
	}
	return "Google"
}

// api names the part of the account a request needs the user to have
// granted, as googleauth.APIs names it, or "" for none den knows.
func api(u *url.URL) string {
	switch u.Hostname() {
	case "gmail.googleapis.com":
		return "Gmail"
	case "docs.googleapis.com", "sheets.googleapis.com":
		return "Drive"
	}
	path := strings.TrimPrefix(strings.TrimPrefix(u.Path, "/"), "upload/")
	switch strings.SplitN(path, "/", 2)[0] {
	case "gmail":
		return "Gmail"
	case "calendar":
		return "Calendar"
	case "drive":
		return "Drive"
	}
	return ""
}

// refuseGoogle turns away what den cannot review, as a batch request
// carries other requests inside it, such as one that sends mail, and what
// the user did not grant, so the agent learns why rather than from
// Google's 403.
func refuseGoogle(f *Fetcher, u *url.URL) string {
	if strings.HasPrefix(u.Path, "/batch") {
		return "den does not pass on batch requests; send each request on its own"
	}
	granted := f.google.Granted()
	if name := api(u); name != "" && granted != nil && !slices.Contains(granted, name) {
		return fmt.Sprintf("the user did not grant den %s; they add it by running den connect google on their computer and allowing it on Google's page", name)
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

// sentTo gives the one address a message sent with messages/send goes to,
// or "".
func sentTo(u *url.URL, body []byte) string {
	if !strings.HasSuffix(u.Path, "/messages/send") {
		return ""
	}
	raw := body
	var wrapped struct {
		Raw string `json:"raw"`
	}
	if json.Unmarshal(body, &wrapped) == nil {
		decoded, err := decodeBase64(wrapped.Raw)
		if wrapped.Raw == "" || err != nil {
			return ""
		}
		raw = decoded
	}
	return letter.Recipient(raw)
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
