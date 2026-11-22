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

	"github.com/unsady/den/internal/approve"
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
	offer:   offerGoogle,
}

// reviewGoogle lets reads through, and the changes den knows to stay with
// the user, as each API's review lists them. Every other change waits for
// the user, so a kind of request den does not know asks too.
func reviewGoogle(ctx context.Context, f *Fetcher, s service, method string, u *url.URL, body []byte) string {
	if method == http.MethodGet || method == http.MethodHead {
		return ""
	}
	if method == http.MethodDelete {
		return fmt.Sprintf("Deletes in %s.", googleSource(u))
	}

	// den reads one value of a query parameter, and Google may act on
	// another.
	for _, values := range u.Query() {
		if len(values) > 1 {
			return "Repeats a query parameter, so den cannot tell what it does."
		}
	}

	name, upload, parts := googlePath(u)
	switch name {
	case "":
		return "Makes a change in Google den does not know."
	case "Gmail":
		return reviewGmail(parts)
	case "Calendar":
		return reviewCalendar(ctx, f, s, u, parts, body)
	}
	return reviewDrive(ctx, f, s, method, u, upload, parts, body)
}

// googlePath splits a request's path into the API it needs, as api names
// it, whether it uploads content, and the path after the API's version, as
// users/me/messages/send for /upload/gmail/v1/users/me/messages/send. The
// API is "" for a version den does not know.
func googlePath(u *url.URL) (name string, upload bool, parts []string) {
	parts = strings.Split(strings.Trim(u.Path, "/"), "/")
	upload = parts[0] == "upload"
	if upload {
		parts = parts[1:]
	}

	name = api(u)
	version := map[string]string{"Gmail": "v1", "Calendar": "v3", "Drive": "v3"}[name]
	switch u.Hostname() {
	case "docs.googleapis.com":
		parts = append([]string{"docs"}, parts...)
		version = "v1"
	case "sheets.googleapis.com":
		parts = append([]string{"sheets"}, parts...)
		version = "v4"
	}
	if version == "" || len(parts) < 2 || parts[1] != version {
		return "", false, nil
	}
	return name, upload, parts[2:]
}

// offerGoogle lets the user allow mail to one address, or edits to one
// file other people can open, for good or for an hour.
func offerGoogle(ctx context.Context, f *Fetcher, s service, method string, u *url.URL, body []byte, q *approve.Question) {
	if to := sentTo(u, body); to != "" {
		q.MailTo(to)
		return
	}

	id := edited(method, u)
	if id == "" {
		return
	}
	var file struct {
		Name string `json:"name"`
	}
	if !f.read(ctx, s, "https://www.googleapis.com/drive/v3/files/"+url.PathEscape(id)+"?supportsAllDrives=true&fields=name", &file) || file.Name == "" {
		file.Name = id
	}
	q.Rule = "fetch google edit " + id
	q.Label = "Edits to " + file.Name
	q.Scope, q.ScopeLabel = q.Rule, q.Label
	q.Text += "\n\nFile: " + file.Name
}

// edited gives the file whose content a request changes and nothing else,
// so that reviewDrive asks about it only because other people can open the
// file, or "": a doc's batchUpdate, a change in one spreadsheet, or new
// content uploaded to a file.
func edited(method string, u *url.URL) string {
	for _, values := range u.Query() {
		if len(values) > 1 {
			return ""
		}
	}
	name, upload, parts := googlePath(u)
	if name != "Drive" || len(parts) < 2 || method == http.MethodDelete {
		return ""
	}

	id := strings.SplitN(parts[1], ":", 2)[0]
	switch {
	case u.Hostname() == "docs.googleapis.com" && len(parts) == 2 && parts[0] == "documents" && strings.HasSuffix(parts[1], ":batchUpdate"):
		return id
	case u.Hostname() == "sheets.googleapis.com" && parts[0] == "spreadsheets" && !strings.HasSuffix(u.Path, ":copyTo"):
		return id
	case u.Hostname() == "www.googleapis.com" && upload && len(parts) == 2 && parts[0] == "files" && u.Query().Get("addParents") == "" &&
		(method == http.MethodPatch || method == http.MethodPut && u.Query().Get("uploadType") == "resumable" && u.Query().Get("upload_id") != ""):
		return id
	}
	return ""
}

// reviewGmail lets labels, the trash, and drafts in the user's own mailbox
// through, and asks before mail goes out.
func reviewGmail(parts []string) string {
	if len(parts) < 3 || parts[0] != "users" || parts[1] != "me" {
		return "Makes a change in Gmail den does not know."
	}

	rest := parts[2:]
	last := rest[len(rest)-1]
	switch {
	case last == "send" && rest[0] == "messages":
		return "Sends an email."
	case last == "send" && rest[0] == "drafts":
		return "Sends a draft."
	// messages/<id>/modify, threads/<id>/trash, messages/batchModify
	case (rest[0] == "messages" || rest[0] == "threads") && len(rest) == 3 && slices.Contains([]string{"modify", "trash", "untrash"}, last):
		return ""
	case rest[0] == "messages" && len(rest) == 2 && last == "batchModify":
		return ""
	// drafts, drafts/<id>, labels, labels/<id>
	case (rest[0] == "drafts" || rest[0] == "labels") && len(rest) <= 2:
		return ""
	}
	return "Makes a change in Gmail den does not know."
}

// reviewCalendar lets the user's own events in their own calendar through:
// events with no one else on them, changed without notifications, not
// cancelled. Another calendar may be someone else's, or shared with them.
func reviewCalendar(ctx context.Context, f *Fetcher, s service, u *url.URL, parts []string, body []byte) string {
	// calendars/<id>/events, calendars/<id>/events/<eventId>
	if len(parts) < 3 || len(parts) > 4 || parts[0] != "calendars" || parts[2] != "events" {
		return "Makes a change in Calendar den does not know."
	}
	if len(parts) == 4 && slices.Contains([]string{"quickAdd", "import", "watch"}, parts[3]) {
		return "Makes a change in Calendar den does not know."
	}

	if !f.own(parts[1]) {
		return "Changes a calendar other than your own, which other people may see."
	}

	q := u.Query()
	if update := q.Get("sendUpdates"); update != "" && update != "none" || q.Get("sendNotifications") == "true" {
		return "Sends other people an invitation or an update."
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
	if len(parts) == 4 && f.guests(ctx, s, u) {
		return "Changes an event that has other people on it."
	}
	return ""
}

// own says whether a calendar id is the user's primary calendar, which
// Google names by their address too.
func (f *Fetcher) own(id string) bool {
	if id == "primary" {
		return true
	}
	return f.google != nil && strings.Contains(id, "@") && strings.EqualFold(id, f.google.Account())
}

// reviewDrive lets through the changes den knows to stay with the user:
// edits to files and comments no one else can open, and new files and
// copies in folders no one else can open. parts is the path after the
// API's version, with docs or sheets before it for those APIs. Every other
// change waits for the user, so a kind of request den does not know, such
// as publishing a doc to the web, asks too.
func reviewDrive(ctx context.Context, f *Fetcher, s service, method string, u *url.URL, upload bool, parts []string, body []byte) string {
	shared := func(ids ...string) bool {
		for _, id := range ids {
			if f.shared(ctx, s, id) {
				return true
			}
		}
		return false
	}
	// A doc's or a sheet's id comes before a colon, as in d1:batchUpdate.
	id := func(i int) string {
		if len(parts) <= i {
			return ""
		}
		return strings.SplitN(parts[i], ":", 2)[0]
	}
	q := u.Query()

	switch u.Hostname() {
	case "docs.googleapis.com":
		// documents, documents/<id>:batchUpdate
		if len(parts) == 1 && parts[0] == "documents" {
			return ""
		}
		if len(parts) == 2 && parts[0] == "documents" && strings.HasSuffix(parts[1], ":batchUpdate") {
			if shared(id(1)) {
				return "Changes a doc other people can open."
			}
			return ""
		}
		return "Makes a change in Docs den does not know."

	case "sheets.googleapis.com":
		// spreadsheets, spreadsheets/<id>..., but a sheet copied to another
		// spreadsheet changes that one.
		if len(parts) == 1 && parts[0] == "spreadsheets" {
			return ""
		}
		if len(parts) >= 2 && parts[0] == "spreadsheets" && !strings.HasSuffix(u.Path, ":copyTo") {
			if shared(id(1)) {
				return "Changes a spreadsheet other people can open."
			}
			return ""
		}
		return "Makes a change in Sheets den does not know."
	}

	// files, files/<id>, files/<id>/copy, files/<id>/comments...
	if len(parts) == 0 || parts[0] != "files" {
		return "Makes a change in Drive den does not know."
	}
	var parents []string
	readParents := func() bool {
		return len(bytes.TrimSpace(body)) == 0 || field(body, "parents", &parents)
	}
	switch {
	// A new file: its metadata names its folder, and a plain upload
	// goes to the top of My Drive. A multipart upload hides the metadata.
	case len(parts) == 1 && method == http.MethodPost:
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

	// The rest of a resumable upload, whose start den looked at; to a file,
	// its content changes too.
	case upload && len(parts) <= 2 && method == http.MethodPut && q.Get("uploadType") == "resumable" && q.Get("upload_id") != "":
		if shared(id(1)) {
			return "Changes a file other people can open."
		}
		return ""

	// A copy without a folder goes to the folders of the file it copies, so
	// den asks unless the body names one.
	case len(parts) == 3 && parts[2] == "copy" && method == http.MethodPost:
		if !readParents() || len(parents) == 0 {
			return "Copies a file without naming its folder in parents, so it may land beside the original."
		}
		if shared(parents...) {
			return "Puts a copy in a folder other people can open."
		}
		return ""

	// A file's metadata or content, and the folders it moves to.
	case len(parts) == 2 && method == http.MethodPatch:
		if shared(id(1)) {
			return "Changes a file other people can open."
		}
		if shared(strings.Split(q.Get("addParents"), ",")...) {
			return "Moves a file to a folder other people can open."
		}
		return ""

	// Comments and replies; a mention mails the person it names.
	case len(parts) >= 3 && parts[2] == "comments" && (method == http.MethodPost || method == http.MethodPatch):
		if shared(id(1)) {
			return "Comments on a file other people can open."
		}
		var content string
		if !field(body, "content", &content) || strings.Contains(content, "@") {
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
