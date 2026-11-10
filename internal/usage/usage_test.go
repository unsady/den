package usage

import (
	"testing"
	"time"
)

func TestReport(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

	s := Open(dir)
	s.Add("chat", now, 120_000, 3_000)
	s.Add("chat", now, 15_500, 400)
	s.Add("jobs", now, 900, 50)
	s.Add("chat", now.AddDate(0, 0, -3), 1_000_000, 10_000)
	s.Add("chat", now.AddDate(0, 0, -20), 2_000_000, 0)
	s.Add("chat", now.AddDate(0, 0, -90), 5, 5)

	want := "Today\n" +
		"chat: 135.5k in, 3.4k out, 2 turns\n" +
		"jobs: 900 in, 50 out, 1 run\n" +
		"\nLast 7 days: 1.1M in, 13.4k out" +
		"\nLast 30 days: 3.1M in, 13.4k out" +
		"\n\nTokens are as fx reports them; some providers do not report every request."
	// A reopened store reads what the first one saved.
	if got := Open(dir).Report(now); got != want {
		t.Fatalf("report:\n%s\nwant:\n%s", got, want)
	}
	if got := Open(dir).Report(now.AddDate(1, 0, 0)); got[:len("Today\nnothing yet")] != "Today\nnothing yet" {
		t.Fatalf("a quiet day: %q", got)
	}
}
