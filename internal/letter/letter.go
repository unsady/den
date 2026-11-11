// Package letter shows an email for the user to read before it goes out:
// its recipients, subject, text, and attachments.
package letter

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"regexp"
	"strings"
)

// Show formats a raw RFC 5322 message. Recipients that the message's
// headers do not name, such as Bcc on an SMTP envelope, are passed as also
// and shown too. It returns "" when raw is not a message.
func Show(raw []byte, also []string) string {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return ""
	}

	var b strings.Builder
	dec := new(mime.WordDecoder)
	named := ""
	// Reply-To and Sender say where answers go, which a reader trusts by
	// the From they see.
	for _, k := range []string{"From", "Sender", "Reply-To", "To", "Cc", "Bcc", "Subject"} {
		v := m.Header.Get(k)
		if v == "" {
			continue
		}
		if d, err := dec.DecodeHeader(v); err == nil {
			v = d
		}
		if k == "To" || k == "Cc" || k == "Bcc" {
			named += strings.ToLower(v) + " "
		}
		fmt.Fprintf(&b, "%s: %s\n", k, v)
	}

	var hidden []string
	for _, to := range also {
		if !strings.Contains(named, strings.ToLower(to)) {
			hidden = append(hidden, to)
		}
	}
	if len(hidden) > 0 {
		fmt.Fprintf(&b, "Bcc: %s\n", strings.Join(hidden, ", "))
	}

	texts, attachments := part(m.Header.Get("Content-Type"), m.Header.Get("Content-Transfer-Encoding"), m.Body)
	b.WriteString("\n" + strings.TrimSpace(texts.show()))
	for _, name := range attachments {
		b.WriteString("\n📎 " + name)
	}

	out := b.String()
	if runes := []rune(out); len(runes) > 1500 {
		out = fmt.Sprintf("%s…\n(%d more characters not shown)", string(runes[:1500]), len(runes)-1500)
	}
	return out
}

// texts are a message's first plain and first HTML text.
type texts struct{ plain, html string }

// show gives the plain text, and the HTML's text too when it says
// something else: most mail apps show the HTML, so the recipient would read
// that rather than the plain text.
func (t texts) show() string {
	html := htmlText(t.html)
	switch {
	case t.plain == "":
		return html
	case html == "" || squash(html) == squash(t.plain):
		return t.plain
	}
	return t.plain + "\n\nThe HTML version, which most mail apps show, reads differently:\n" + html
}

var (
	hiddenTags = regexp.MustCompile(`(?is)<(style|script|head)\b.*?</(style|script|head)>`)
	tags       = regexp.MustCompile(`(?s)<[^>]*>`)
	breaks     = regexp.MustCompile(`(?i)<(br|/p|/div|/li|/h\d|/tr)\b[^>]*>`)
)

// htmlText is the text an HTML part shows, its tags left out.
func htmlText(markup string) string {
	if markup == "" {
		return ""
	}
	text := hiddenTags.ReplaceAllString(markup, "")
	text = breaks.ReplaceAllString(text, "\n")
	text = html.UnescapeString(tags.ReplaceAllString(text, ""))
	var lines []string
	for line := range strings.SplitSeq(text, "\n") {
		if line = strings.Join(strings.Fields(line), " "); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func squash(text string) string { return strings.Join(strings.Fields(text), " ") }

// part returns a message part's first plain and HTML texts and the names
// of its attachments.
func part(contentType, encoding string, body io.Reader) (texts, []string) {
	media, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		media = "text/plain"
	}

	if strings.HasPrefix(media, "multipart/") {
		var found texts
		var attachments []string
		r := multipart.NewReader(body, params["boundary"])
		for {
			p, err := r.NextPart()
			if err != nil {
				break
			}
			if name := p.FileName(); name != "" {
				attachments = append(attachments, name)
				continue
			}
			t, more := part(p.Header.Get("Content-Type"), p.Header.Get("Content-Transfer-Encoding"), p)
			if found.plain == "" {
				found.plain = t.plain
			}
			if found.html == "" {
				found.html = t.html
			}
			attachments = append(attachments, more...)
		}
		return found, attachments
	}
	if media != "text/plain" && media != "text/html" {
		return texts{}, nil
	}

	switch strings.ToLower(encoding) {
	case "base64":
		body = base64.NewDecoder(base64.StdEncoding, &newlineSkipper{body})
	case "quoted-printable":
		body = quotedprintable.NewReader(body)
	}
	data, _ := io.ReadAll(io.LimitReader(body, 1<<20))
	if media == "text/html" {
		return texts{html: string(data)}, nil
	}
	return texts{plain: string(data)}, nil
}

// newlineSkipper drops line breaks, which base64 in mail is wrapped with.
type newlineSkipper struct{ r io.Reader }

func (n *newlineSkipper) Read(p []byte) (int, error) {
	k, err := n.r.Read(p)
	j := 0
	for _, c := range p[:k] {
		if c != '\r' && c != '\n' {
			p[j] = c
			j++
		}
	}
	return j, err
}
