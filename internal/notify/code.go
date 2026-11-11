package notify

import (
	"regexp"
	"strings"
	"unicode/utf16"
)

// The agent writes plain text, but marks text to copy the way Markdown
// does: a ``` block or `inline code`. Telegram shows those as code, which
// a tap copies, when the message names them as entities; the marks
// themselves are left out. A Markdown link, [text](url), becomes a link
// the same way.

// entity is a part of a message Telegram formats, in UTF-16 code units.
type entity struct {
	Type     string `json:"type"`
	Offset   int    `json:"offset"`
	Length   int    `json:"length"`
	Language string `json:"language,omitempty"`
	URL      string `json:"url,omitempty"`
}

var (
	fence  = regexp.MustCompile("(?s)```([\\w+#.-]*)[ \\t]*\\n?(.*?)\\n?```")
	inline = regexp.MustCompile("`([^`\\n]+)`|\\[([^\\]\\n]+)\\]\\((https?://(?:[^()\\s]|\\([^()\\s]*\\))+)\\)")
)

// code takes the code marks out of text and returns the text with the
// entities that format what they marked.
func code(text string) (string, []entity) {
	var out strings.Builder
	var entities []entity
	add := func(e entity, body string) {
		if body == "" {
			return
		}
		e.Offset, e.Length = units(out.String()), units(body)
		entities = append(entities, e)
		out.WriteString(body)
	}

	rest := text
	for rest != "" {
		block := fence.FindStringSubmatchIndex(rest)
		end := len(rest)
		if block != nil {
			end = block[0]
		}
		plain := rest[:end]
		for plain != "" {
			m := inline.FindStringSubmatchIndex(plain)
			if m == nil {
				out.WriteString(plain)
				break
			}
			out.WriteString(plain[:m[0]])
			if m[2] >= 0 {
				add(entity{Type: "code"}, plain[m[2]:m[3]])
			} else {
				add(entity{Type: "text_link", URL: plain[m[6]:m[7]]}, plain[m[4]:m[5]])
			}
			plain = plain[m[1]:]
		}
		if block == nil {
			break
		}
		add(entity{Type: "pre", Language: rest[block[2]:block[3]]}, rest[block[4]:block[5]])
		rest = rest[block[1]:]
	}
	return out.String(), entities
}

// units counts text in UTF-16 code units, as Telegram measures entities.
func units(text string) int {
	return len(utf16.Encode([]rune(text)))
}
