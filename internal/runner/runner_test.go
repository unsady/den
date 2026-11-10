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
