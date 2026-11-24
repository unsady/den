package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/store"
	"github.com/unsady/den/internal/today"
)

func TestNewDay(t *testing.T) {
	cfg := config.Config{Space: t.TempDir(), State: t.TempDir()}
	db, err := store.Open(filepath.Join(cfg.State, store.File))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := &daemon{ctx: context.Background(), cfg: cfg, db: db}

	path := filepath.Join(cfg.Space, today.File)
	write := func() {
		if err := os.WriteFile(path, []byte("- [ ] 15:00 Interview\n- [ ] Buy oat milk\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func() string {
		data, _ := os.ReadFile(path)
		return string(data)
	}
	write()

	// The first day den sees keeps the list: it may be that day's.
	day := time.Date(2026, 11, 24, 10, 0, 0, 0, time.Local)
	d.newDay(day)
	if read() != "- [ ] 15:00 Interview\n- [ ] Buy oat milk\n" {
		t.Fatalf("first day: %q", read())
	}
	d.newDay(day.Add(time.Hour))
	if read() != "- [ ] 15:00 Interview\n- [ ] Buy oat milk\n" {
		t.Fatalf("same day: %q", read())
	}

	// A new day, though den saw its midnight late, drops the timed item.
	d.newDay(day.Add(20 * time.Hour))
	if read() != "- [ ] Buy oat milk\n" {
		t.Fatalf("new day: %q", read())
	}
	// What the agent adds later that day stays.
	write()
	d.newDay(day.Add(22 * time.Hour))
	if read() != "- [ ] 15:00 Interview\n- [ ] Buy oat milk\n" {
		t.Fatalf("later that day: %q", read())
	}
}
