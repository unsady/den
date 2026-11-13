package daemon

import (
	"testing"
	"time"
)

func TestRunsText(t *testing.T) {
	now := time.Now()
	runs := map[string]*mapRun{
		"b": {label: "muse-spark-1.3", started: now, done: 3, total: 8},
		"a": {label: "deepseek-v4.1-flash", started: now.Add(-time.Minute), done: 38, total: 54, failed: 1},
	}
	want := "🗂 Running\ndeepseek-v4.1-flash · 38 of 54 · 1 failed\nmuse-spark-1.3 · 3 of 8"
	if got := runsText(runs); got != want {
		t.Fatalf("got %q", got)
	}
}

func TestWithoutProgress(t *testing.T) {
	out := "Asking a/b about each item of x.jsonl…\nden map: 3 of 9 done, 0 failed, $0.0000 so far\nnot run: quota\n"
	if got := withoutProgress(out); got != "not run: quota\n" {
		t.Fatalf("got %q", got)
	}
}
