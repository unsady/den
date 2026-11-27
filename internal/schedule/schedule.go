// Package schedule parses the schedule forms den accepts.
//
// One-shot forms: "in 30m", "in 1h30m", or a local time such as
// "2026-11-10 09:00". Recurring forms: "every 2h", a five-field cron
// expression such as "0 9 * * 1-5", or a descriptor such as "@daily".
package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

var timeLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
}

// First returns the first run of spec after now and whether spec runs once.
func First(spec string, now time.Time) (time.Time, bool, error) {
	s := strings.TrimSpace(spec)

	if rest, ok := cutPrefixFold(s, "in "); ok {
		d, err := time.ParseDuration(strings.ReplaceAll(rest, " ", ""))
		if err != nil || d <= 0 {
			return time.Time{}, false, fmt.Errorf("schedule %q: want a duration such as \"in 30m\"", spec)
		}
		return now.Add(d), true, nil
	}

	for _, layout := range timeLayouts {
		t, err := time.ParseInLocation(layout, s, time.Local)
		if err != nil {
			continue
		}
		if !t.After(now) {
			return time.Time{}, false, fmt.Errorf("schedule %q is in the past", spec)
		}
		return t, true, nil
	}

	next, err := Next(s, now)
	return next, false, err
}

// Next returns the run of a recurring spec that follows after.
func Next(spec string, after time.Time) (time.Time, error) {
	s := strings.TrimSpace(spec)
	if rest, ok := cutPrefixFold(s, "every "); ok {
		s = "@every " + strings.ReplaceAll(rest, " ", "")
	}

	sched, err := parser.Parse(s)
	if err != nil {
		return time.Time{}, fmt.Errorf("schedule %q: %w", spec, err)
	}

	next := sched.Next(after)
	if next.IsZero() {
		return time.Time{}, fmt.Errorf("schedule %q never runs", spec)
	}
	return next, nil
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return s, false
	}
	return strings.TrimSpace(s[len(prefix):]), true
}

// days names the days of the week as cron numbers them, 7 being Sunday too.
var days = []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}

// Describe says a recurring spec in words, such as "Every day at 09:00",
// or gives the spec back without its time zone when it has no simple form.
func Describe(spec string) string {
	s := strings.TrimSpace(spec)
	if rest, ok := cutPrefixFold(s, "every "); ok {
		return "Every " + rest
	}
	switch s {
	case "@hourly":
		return "Every hour"
	case "@daily", "@midnight":
		return "Every day at 00:00"
	case "@weekly":
		return "Every Sun at 00:00"
	case "@monthly":
		return "Every month"
	case "@yearly", "@annually":
		return "Every year"
	}
	if strings.HasPrefix(s, "CRON_TZ=") || strings.HasPrefix(s, "TZ=") {
		_, s, _ = strings.Cut(s, " ")
		s = strings.TrimSpace(s)
	}

	f := strings.Fields(s)
	if len(f) != 5 || f[2] != "*" || f[3] != "*" {
		return s
	}
	minute, ok := number(f[0], 59)
	if !ok {
		return s
	}
	if f[1] == "*" && f[4] == "*" {
		return fmt.Sprintf("Every hour at :%02d", minute)
	}
	hour, ok := number(f[1], 23)
	if !ok {
		return s
	}
	at := fmt.Sprintf("at %02d:%02d", hour, minute)

	switch f[4] {
	case "*":
		return "Every day " + at
	case "1-5":
		return "Every weekday " + at
	case "0,6", "6,0", "6,7":
		return "Every weekend " + at
	}
	var names []string
	for _, d := range strings.Split(f[4], ",") {
		n, ok := number(d, 7)
		if !ok {
			return s
		}
		names = append(names, days[n])
	}
	return "Every " + strings.Join(names, ", ") + " " + at
}

// number reads a cron field that is one number no greater than limit.
func number(field string, limit int) (int, bool) {
	n, err := strconv.Atoi(field)
	return n, err == nil && n >= 0 && n <= limit
}
