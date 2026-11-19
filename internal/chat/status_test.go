package chat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/jobs"
)

func TestJobsLinesSumUpAndNameFailures(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.Local)
	ran := now.Add(-time.Hour)
	list := []jobs.Job{
		{Name: "nightly-reflect", Title: "Reflect", NextRun: now.Add(4 * time.Hour), LastRun: &ran, LastStatus: "ok"},
		{Name: "news-alerts", Title: "Новости", NextRun: now.Add(70 * time.Minute), LastRun: &ran, LastStatus: "ok"},
		{Name: "model-speed", Title: "Скорость моделей", NextRun: now.Add(3 * time.Hour), LastRun: &ran, LastStatus: "failed"},
	}
	got := strings.Join(jobsLines(list), "\n")
	want := "Jobs: 3 · next Новости at 01:10\n✗ Скорость моделей failed Nov 10 23:00"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if jobsLines(nil) != nil {
		t.Fatal("no jobs: want no lines")
	}
}

func TestWindowOfCodexSubscription(t *testing.T) {
	dir := t.TempDir()
	c := &Chat{cfg: config.Config{State: dir, Sessions: filepath.Join(dir, "sessions", "v2")}}
	if err := os.WriteFile(filepath.Join(dir, "codex-windows.json"), []byte(`{"gpt-6.1-sol":500000}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := c.window("Codex subscription", "gpt-6.1-sol"); got != 500000 {
		t.Fatalf("got %d", got)
	}
}
