package widget

import (
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	good := `{"type":"chart","kind":"line","title":"Speed","labels":["a","b"],"series":[{"name":"x","values":[1,2.5]}]}`
	if out, err := Check([]byte(good)); err != nil || !strings.Contains(string(out), `"values":[1,2.5]`) {
		t.Fatalf("good: %s, %v", out, err)
	}

	for name, bad := range map[string]string{
		"type":    `{"type":"table","kind":"line","labels":["a"],"series":[{"values":[1]}]}`,
		"kind":    `{"type":"chart","kind":"pie","labels":["a"],"series":[{"values":[1]}]}`,
		"count":   `{"type":"chart","kind":"bar","labels":["a","b"],"series":[{"values":[1]}]}`,
		"empty":   `{"type":"chart","kind":"bar","labels":[],"series":[]}`,
		"unknown": `{"type":"chart","kind":"bar","labels":["a"],"series":[{"values":[1]}],"colors":["red"]}`,
		"json":    `{"type":`,
	} {
		if _, err := Check([]byte(bad)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
