package claude

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/unsady/den/internal/config"
)

// Version is the claude release den runs. Raising it makes den download
// the new one at its next start.
const Version = "2.1.311"

// sums are the sha256 of the pinned release's binary, by GOARCH.
var sums = map[string]string{
	"arm64": "2e2496c9a8065df93acb0fca2d341f59a6b8777649b2e025ab8c9bcd5eef61f3",
	"amd64": "86dc1fcc69498159eaf15f446613e847010f9036bfbbdffd4c49350be8489ada",
}

// platforms name the release's build for each GOARCH.
var platforms = map[string]string{
	"arm64": "linux-arm64",
	"amd64": "linux-x64",
}

// Releases is where claude is downloaded from. Tests point it at a local
// server.
var Releases = "https://downloads.claude.ai/claude-code-releases"

// checkTimeout bounds a call of Check.
const checkTimeout = 2 * time.Minute

// Path is where the pinned claude lives once downloaded.
func Path(cfg config.Config) string {
	return filepath.Join(cfg.State, "claude", Version, "claude")
}

// ConfigDir is Claude's own state, which only den reads.
func ConfigDir(cfg config.Config) string {
	return filepath.Join(cfg.State, "claude", "config")
}

// WorkDir is where claude runs, which its transcripts are filed under.
func WorkDir(cfg config.Config) string {
	return filepath.Join(cfg.State, "claude", "work")
}

// Install downloads the pinned claude when it is missing, checks it
// against its known hash, and removes the versions it replaces.
func (c *Claude) Install(ctx context.Context) error {
	path := Path(c.cfg)
	if _, err := os.Stat(path); err == nil {
		return nil
	}

	sum, ok := sums[runtime.GOARCH]
	if !ok {
		return fmt.Errorf("Claude has no build for %s", runtime.GOARCH)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	log.Printf("claude: downloading %s", Version)
	n, err := download(ctx, Releases+"/"+Version+"/"+platforms[runtime.GOARCH]+"/claude", path, sum)
	if err != nil {
		return fmt.Errorf("downloading Claude %s: %w", Version, err)
	}
	log.Printf("claude: downloaded %s (%d MB)", Version, n>>20)

	root := filepath.Dir(filepath.Dir(path))
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.Name() != Version && e.Name() != filepath.Base(ConfigDir(c.cfg)) && e.Name() != filepath.Base(WorkDir(c.cfg)) {
			os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
	return nil
}

// download saves url to path if its contents have the sha256 sum.
func download(ctx context.Context, url, path, sum string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	part := path + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		return 0, err
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), resp.Body)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil && hex.EncodeToString(hash.Sum(nil)) != sum {
		err = errors.New("the download does not match its known hash")
	}
	if err == nil {
		err = os.Chmod(part, 0o700)
	}
	if err == nil {
		err = os.Rename(part, path)
	}
	if err != nil {
		os.Remove(part)
		return 0, err
	}
	return n, nil
}

// Env is the whole environment claude runs in. It starts empty, so nothing
// of den's reaches it, and holds the token and the settings the bridge
// needs.
func (c *Claude) Env(token string) []string {
	return []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + filepath.Join(c.cfg.State, "claude"),
		"LANG=C.UTF-8",
		"CLAUDE_CODE_OAUTH_TOKEN=" + token,
		"CLAUDE_CONFIG_DIR=" + ConfigDir(c.cfg),
		"CLAUDE_CODE_DISABLE_CLAUDE_MDS=1",
		"CLAUDE_CODE_DISABLE_AUTO_MEMORY=1",
		"DISABLE_AUTO_COMPACT=1",
		"CLAUDE_CODE_ENABLE_AWAY_SUMMARY=0",
		"ENABLE_TOOL_SEARCH=false",
		"CLAUDE_CODE_DISABLE_ADVISOR_TOOL=1",
		"CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK=1",
		"CLAUDE_CODE_DISABLE_REFUSAL_FALLBACK=1",
		"MAX_MCP_OUTPUT_TOKENS=200000",
		"CLAUDE_CODE_POLISHED_DEWDROP=drop",
		"DISABLE_AUTOUPDATER=1",
		"MCP_TOOL_TIMEOUT=86400000",
	}
}

// Check asks Claude for a short reply with the claude command, without
// tools, to see that the token works. It runs the command itself, not
// through den's proxy, since the daemon may not have the new token yet.
func (c *Claude) Check(ctx context.Context) error {
	token, err := c.Token()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	dir, err := os.MkdirTemp("", "claude-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := os.MkdirAll(ConfigDir(c.cfg), 0o700); err != nil {
		return err
	}

	bin := c.bin
	if bin == "" {
		bin = Path(c.cfg)
	}
	cmd := exec.CommandContext(ctx, bin, "-p", "--output-format", "json", "--tools", "",
		"--setting-sources", "", "--strict-mcp-config", "--model", "haiku", "Reply with OK.")
	cmd.Dir = dir
	cmd.Env = c.Env(token)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	if ctx.Err() != nil {
		return fmt.Errorf("Claude did not answer in %s", checkTimeout)
	}

	// A failed call still exits 0 and says why in the JSON.
	var out struct {
		IsError bool   `json:"is_error"`
		Result  string `json:"result"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		if runErr != nil {
			return fmt.Errorf("claude failed: %v: %s", runErr, clip(stderr.String()))
		}
		return fmt.Errorf("claude answered without JSON: %s", clip(stdout.String()))
	}
	if out.IsError {
		return errors.New(out.Result)
	}
	return nil
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}
