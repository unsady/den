package runner

import "testing"

func TestLegacySilent(t *testing.T) {
	for reply, want := range map[string]bool{
		"SILENT":                    true,
		"  SILENT\n":                true,
		"Checked the feed.\nSILENT": false,
		"SILENT, nothing to send":   false,
		"silent":                    false,
		"":                          false,
	} {
		if got := LegacySilent(reply, "test"); got != want {
			t.Errorf("LegacySilent(%q) = %v, want %v", reply, got, want)
		}
	}
}
