package runner

import "testing"

func TestProviderFailed(t *testing.T) {
	for stderr, want := range map[string]bool{
		"Reading a.txt\nfx ask: API request failed · HTTP 402 · out of credits": true,
		"fx ask: API access denied · HTTP 401 · token expired":                  true,
		"fx ask: API request failed · HTTP 429 · rate limited":                  true,
		"fx ask: API request failed · HTTP 503 · overloaded":                    true,
		"fx ask: API request failed · HTTP 400 · prompt is too long":            false,
		"Running curl -w 'HTTP 500'\nfx ask: the turn ran out of steps":         false,
		"": false,
	} {
		if got := ProviderFailed(stderr); got != want {
			t.Errorf("ProviderFailed(%q) = %v, want %v", stderr, got, want)
		}
	}
}

func TestIsSilent(t *testing.T) {
	for reply, want := range map[string]bool{
		"SILENT":                         true,
		"  SILENT\n":                     true,
		"All sources healthy.\n\nSILENT": true,
		"No new jobs.":                   false,
		"SILENT means nothing to send.":  false,
		"Say SILENT to skip.\nOK":        false,
		"":                               false,
	} {
		if got := IsSilent(reply); got != want {
			t.Errorf("IsSilent(%q) = %v", reply, got)
		}
	}
}
