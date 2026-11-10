package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// den update also updates this den command: from the latest release, the
// one whose image :latest is, or from the checkout den builds its image
// from.

// releases is where install.sh and den update download den.
const releases = "https://github.com/unsady/den/releases/latest/download"

// updateCommand replaces the running den with the new one, when there is
// one.
func updateCommand(c hostConfig) error {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return err
	}

	if c.Source != "" {
		return buildCommand(c.Source, exe)
	}

	base := releases
	if b := os.Getenv("DEN_DOWNLOAD"); b != "" {
		base = b
	}
	var changed bool
	err = step("Checking for a new den", func(ctx context.Context) error {
		changed, err = download(ctx, base, exe)
		return err
	})
	switch {
	case errors.Is(err, os.ErrPermission):
		failed("Cannot replace %s: run the install line again", exe)
		return nil
	case err != nil:
		return err
	case changed:
		done("den command updated")
	default:
		done("den command is up to date")
	}
	return nil
}

// download fetches this system's den from base and puts it in place of
// exe when its checksum differs.
func download(ctx context.Context, base, exe string) (bool, error) {
	name := "den-" + runtime.GOOS + "-" + runtime.GOARCH
	sums, err := get(ctx, base+"/SHA256SUMS")
	if err != nil {
		return false, err
	}
	want := ""
	scan := bufio.NewScanner(strings.NewReader(string(sums)))
	for scan.Scan() {
		if f := strings.Fields(scan.Text()); len(f) == 2 && f[1] == name {
			want = f[0]
		}
	}
	if want == "" {
		return false, fmt.Errorf("the release has no %s", name)
	}

	current, err := os.ReadFile(exe)
	if err != nil {
		return false, err
	}
	if sum(current) == want {
		return false, nil
	}

	binary, err := get(ctx, base+"/"+name)
	if err != nil {
		return false, err
	}
	if sum(binary) != want {
		return false, errors.New("the download does not match its checksum")
	}
	return true, replace(exe, binary)
}

func get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 200<<20))
}

func sum(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

// replace writes binary next to exe and renames it over exe, so a running
// den keeps its old file and the next one starts the new.
func replace(exe string, binary []byte) error {
	tmp := exe + ".new"
	if err := os.WriteFile(tmp, binary, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// buildCommand builds den from the checkout in place of exe, when Go is
// here to build it.
func buildCommand(source, exe string) error {
	if _, err := exec.LookPath("go"); err != nil {
		note("den builds its image from " + source + "; build the den command there with go build.")
		return nil
	}
	err := step("Building the den command from "+source, func(context.Context) error {
		cmd := exec.Command("go", "build", "-trimpath", "-o", exe+".new", "./cmd/den")
		cmd.Dir = source
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("go build: %v\n%s", err, lastLines(string(out), 20))
		}
		return os.Rename(exe+".new", exe)
	})
	if err != nil {
		return err
	}
	done("den command built from %s", source)
	return nil
}
