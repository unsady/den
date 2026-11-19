package models

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// grokVersionFor is how long den trusts the Grok CLI version it learned.
const grokVersionFor = 6 * time.Hour

// grokVersionPattern is the shape of a version, so what xAI's page says
// goes into a header only when it is one.
var grokVersionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// grokVersion is the current Grok CLI version, which the subscription's API
// wants with each request and refuses one without. fx learns it from the
// same page and sends it itself; den's own requests, such as den map's, get
// it here. A version learned earlier stands in while the page cannot be
// read.
func (m *Models) grokVersion(ctx context.Context) (string, error) {
	m.grokVersionMu.Lock()
	defer m.grokVersionMu.Unlock()

	if m.grokVersionValue != "" && time.Since(m.grokVersionAt) < grokVersionFor {
		return m.grokVersionValue, nil
	}
	version, err := m.fetchGrokVersion(ctx)
	if err != nil {
		if m.grokVersionValue != "" {
			return m.grokVersionValue, nil
		}
		return "", fmt.Errorf("den could not learn the current Grok CLI version from %s: %w", m.grokRelease, err)
	}
	m.grokVersionValue, m.grokVersionAt = version, time.Now()
	return version, nil
}

func (m *Models) fetchGrokVersion(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.grokRelease, nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Transport: m.transport}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(string(data))
	if !grokVersionPattern.MatchString(version) {
		return "", fmt.Errorf("%q is not a version", version)
	}
	return version, nil
}
