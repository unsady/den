package schedule

import (
	"testing"
	"time"
)

func TestFirst(t *testing.T) {
	now := time.Date(2026, 11, 9, 10, 0, 0, 0, time.Local)

	cases := []struct {
		spec string
		want time.Time
		once bool
	}{
		{"in 30m", now.Add(30 * time.Minute), true},
		{"In 1h 30m", now.Add(90 * time.Minute), true},
		{"2026-11-10 09:00", time.Date(2026, 11, 10, 9, 0, 0, 0, time.Local), true},
		{"every 2h", now.Add(2 * time.Hour), false},
		{"0 9 * * *", time.Date(2026, 11, 10, 9, 0, 0, 0, time.Local), false},
		{"@daily", time.Date(2026, 11, 10, 0, 0, 0, 0, time.Local), false},
	}

	for _, c := range cases {
		got, once, err := First(c.spec, now)
		if err != nil {
			t.Fatalf("%q: %v", c.spec, err)
		}
		if !got.Equal(c.want) || once != c.once {
			t.Errorf("%q: got %v once=%v, want %v once=%v", c.spec, got, once, c.want, c.once)
		}
	}
}

func TestFirstRejects(t *testing.T) {
	now := time.Date(2026, 11, 9, 10, 0, 0, 0, time.Local)

	for _, spec := range []string{"in soon", "2026-11-08 09:00", "tomorrow", "in -5m"} {
		if _, _, err := First(spec, now); err == nil {
			t.Errorf("%q: want an error", spec)
		}
	}
}
