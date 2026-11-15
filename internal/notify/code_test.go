package notify

import (
	"reflect"
	"testing"
)

func TestCode(t *testing.T) {
	for _, c := range []struct {
		in, out  string
		entities []entity
	}{
		{"no marks here", "no marks here", nil},
		{"Copy this:\n```text\nI’ve been putting it off.\n```\nThat's all.", "Copy this:\nI’ve been putting it off.\nThat's all.",
			[]entity{{Type: "pre", Offset: 11, Length: 25, Language: "text"}}},
		{"Run `den update` then `den status`.", "Run den update then den status.",
			[]entity{{Type: "code", Offset: 4, Length: 10}, {Type: "code", Offset: 20, Length: 10}}},
		{"😀 `x`", "😀 x", []entity{{Type: "code", Offset: 3, Length: 1}}},
		{"```\nls -la\n``` and `pwd`", "ls -la and pwd",
			[]entity{{Type: "pre", Offset: 0, Length: 6}, {Type: "code", Offset: 11, Length: 3}}},
		{"an unmatched ` stays", "an unmatched ` stays", nil},
		{"See [the book](https://example.com/a_(b)) and `x`.", "See the book and x.",
			[]entity{{Type: "text_link", Offset: 4, Length: 8, URL: "https://example.com/a_(b)"}, {Type: "code", Offset: 17, Length: 1}}},
		{"[Öffnen](https://example.com/en)\nhttps://example.com/raw", "Öffnen\nhttps://example.com/raw",
			[]entity{{Type: "text_link", Offset: 0, Length: 6, URL: "https://example.com/en"}}},
		{"[not a link](notes/a.md)", "[not a link](notes/a.md)", nil},
		{"- **Größe:** den connect claude", "- Größe: den connect claude",
			[]entity{{Type: "bold", Offset: 2, Length: 6}}},
		{"**a** and `**b**`", "a and **b**", []entity{{Type: "bold", Offset: 0, Length: 1}, {Type: "code", Offset: 6, Length: 5}}},
		{"2 ** 3 and 4 ** 5", "2 ** 3 and 4 ** 5", nil},
		{"an unmatched ** stays", "an unmatched ** stays", nil},
	} {
		out, entities := code(c.in)
		if out != c.out || !reflect.DeepEqual(entities, c.entities) {
			t.Errorf("code(%q)\n= %q %+v\nwant %q %+v", c.in, out, entities, c.out, c.entities)
		}
	}
}
