// Package fxsettings changes fx's settings in the agent's profile, the
// providers and models fx uses.
//
// The daemon and den's commands, such as den login, change them from more
// than one place at once, each reading the whole file and writing it back.
// Edit takes a lock first, so one change never writes over another, and
// changes nothing when the file is there but den cannot read it, so a
// failed read never leaves fx with nothing but the change.
package fxsettings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/proc"
)

// Path is fx's settings file in the agent's profile, ~/.fx/settings.json.
func Path(cfg config.Config) string {
	return filepath.Join(cfg.Sessions, "..", "..", "settings.json")
}

// Edit calls change with fx's settings, and writes them back when it
// reports a change. Settings start empty only when the file does not exist.
func Edit(cfg config.Config, change func(settings map[string]json.RawMessage) (bool, error)) error {
	unlock, err := lock(cfg)
	if err != nil {
		return err
	}
	defer unlock()

	// The profile is the agent's: den reads and writes it as the agent, so
	// a link there cannot lead den to its own files.
	path := Path(cfg)
	settings := map[string]json.RawMessage{}
	data, err := proc.ReadFile(cfg, path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("%s: %v", path, err)
		}
	case missing(path):
	default:
		return err
	}

	changed, err := change(settings)
	if err != nil || !changed {
		return err
	}
	out, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	return proc.WriteFile(cfg, path, out, 0o600)
}

// missing reports that nothing is at path, not even a link.
func missing(path string) bool {
	_, err := os.Lstat(path)
	return errors.Is(err, fs.ErrNotExist)
}

// lock holds den's lock on fx's settings until the returned function
// runs. The lock file is in den's state, where the agent cannot take it.
func lock(cfg config.Config) (func(), error) {
	if err := os.MkdirAll(cfg.State, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(cfg.State, "fx-settings.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("locking fx's settings: %w", err)
	}
	return func() { f.Close() }, nil
}
