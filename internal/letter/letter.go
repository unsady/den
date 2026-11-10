// Package letter shows an email for the user to read before it goes out:
// its recipients, subject, text, and attachments.
package letter

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
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
	for _, k := range []string{"From", "To", "Cc", "Bcc", "Subject"} {
		v := m.Header.Get(k)
		if v == "" {
			continue
		}
		if d, err := dec.DecodeHeader(v); err == nil {
			v = d
		}
		if k != "Subject" {
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

	text, attachments := part(m.Header.Get("Content-Type"), m.Header.Get("Content-Transfer-Encoding"), m.Body)
	b.WriteString("\n" + strings.TrimSpace(text))
	for _, name := range attachments {
		b.WriteString("\n📎 " + name)
	}

	out := b.String()
	if runes := []rune(out); len(runes) > 1500 {
		out = string(runes[:1500]) + "…"
	}
	return out
}

// part returns a message part's plain text and the names of its
// attachments.
func part(contentType, encoding string, body io.Reader) (string, []string) {
	media, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		media = "text/plain"
	}

	if strings.HasPrefix(media, "multipart/") {
		var text string
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
			if text == "" {
				text = t
			}
			attachments = append(attachments, more...)
		}
		return text, attachments
	}
	if media != "text/plain" && media != "text/html" {
		return "", nil
	}

	switch strings.ToLower(encoding) {
	case "base64":
		body = base64.NewDecoder(base64.StdEncoding, &newlineSkipper{body})
	case "quoted-printable":
		body = quotedprintable.NewReader(body)
	}
	data, _ := io.ReadAll(io.LimitReader(body, 1<<20))
	text := string(data)
	if media == "text/html" {
		text = "(HTML) " + text
	}
	return text, nil
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
