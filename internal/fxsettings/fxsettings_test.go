package fxsettings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/unsady/den/internal/config"
)

func testConfig(t *testing.T) config.Config {
	root := t.TempDir()
	return config.Config{State: filepath.Join(root, "state"), Sessions: filepath.Join(root, "home", ".fx", "sessions", "v2")}
}

func set(key string) func(map[string]json.RawMessage) (bool, error) {
	return func(s map[string]json.RawMessage) (bool, error) {
		s[key] = json.RawMessage(`true`)
		return true, nil
	}
}

func TestEdit(t *testing.T) {
	cfg := testConfig(t)

	// A missing file starts empty.
	if err := Edit(cfg, set("first")); err != nil {
		t.Fatal(err)
	}

	// Changes at once all stay.
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			if err := Edit(cfg, set(fmt.Sprint("key", i))); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	data, _ := os.ReadFile(Path(cfg))
	var got map[string]bool
	json.Unmarshal(data, &got)
	if len(got) != 9 || !got["first"] || !got["key7"] {
		t.Fatalf("settings %s", data)
	}
}

func TestEditKeepsWhatItCannotRead(t *testing.T) {
	cfg := testConfig(t)

	// A file den cannot read, here a directory, is left as it is.
	if err := os.MkdirAll(Path(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Edit(cfg, set("lost")); err == nil {
		t.Fatal("an unreadable file was written over")
	}
	if info, err := os.Stat(Path(cfg)); err != nil || !info.IsDir() {
		t.Fatalf("the file changed: %v", err)
	}

	// Nor is one that is not JSON.
	os.Remove(Path(cfg))
	os.WriteFile(Path(cfg), []byte("{broken"), 0o600)
	if err := Edit(cfg, set("lost")); err == nil {
		t.Fatal("a broken file was written over")
	}
	if data, _ := os.ReadFile(Path(cfg)); string(data) != "{broken" {
		t.Fatalf("settings %s", data)
	}
}
