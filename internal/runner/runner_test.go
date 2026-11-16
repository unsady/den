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
		if got := providerFailed(0, stderr); got != want {
			t.Errorf("providerFailed(0, %q) = %v, want %v", stderr, got, want)
		}
	}

	// The status fx reports wins over what it wrote.
	for status, want := range map[int]bool{401: true, 402: true, 429: true, 503: true, 400: false, 404: false} {
		if got := providerFailed(status, "fx ask: API request failed · HTTP 429 · rate limited"); got != want {
			t.Errorf("providerFailed(%d) = %v, want %v", status, got, want)
		}
	}
}
