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
	// An hour with no data is null, and stays null.
	gap := `{"type":"chart","kind":"line","title":"Speed","labels":["a","b","c"],"series":[{"name":"x","values":[1,null,3]}]}`
	if out, err := Check([]byte(gap)); err != nil || !strings.Contains(string(out), `"values":[1,null,3]`) {
		t.Fatalf("gap: %s, %v", out, err)
	}

	for name, bad := range map[string]string{
		"type":    `{"type":"table","kind":"line","labels":["a"],"series":[{"values":[1]}]}`,
		"kind":    `{"type":"chart","kind":"pie","labels":["a"],"series":[{"values":[1]}]}`,
		"count":   `{"type":"chart","kind":"bar","labels":["a","b"],"series":[{"values":[1]}]}`,
		"empty":   `{"type":"chart","kind":"bar","labels":[],"series":[]}`,
		"unknown": `{"type":"chart","kind":"bar","labels":["a"],"series":[{"values":[1]}],"colors":["red"]}`,
		"json":    `{"type":`,
		"nulls":   `{"type":"chart","kind":"line","labels":["a","b"],"series":[{"values":[null,null]}]}`,
	} {
		if _, err := Check([]byte(bad)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
