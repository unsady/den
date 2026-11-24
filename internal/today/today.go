// Package today gathers the user's day for the app's Today view: the
// events of their Google Calendar, the reminders still to come today,
// the replies of the jobs that ran in the night's quiet hours, and the
// to-dos the agent keeps in the space's today.md, which the user ticks off
// in the app.
package today

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/proc"
)

// File is the to-do list in the space.
const File = "today.md"

// ErrChanged says the to-do the app ticked is no longer where it was: the
// agent changed the list since the app read it.
var ErrChanged = errors.New("the list changed since it was read")

// Day is what the app's Today view shows.
type Day struct {
	// Date is the day's date, such as 2026-11-23.
	Date   string  `json:"date"`
	Events []Event `json:"events"`
	// Calendar says why the events are missing when the calendar is
	// connected but den could not read it.
	Calendar string   `json:"calendar,omitempty"`
	Jobs     []Job    `json:"jobs"`
	Night    []Result `json:"night"`
	Tasks    []Task   `json:"tasks"`
}

// Event is an event of the user's calendar.
type Event struct {
	Title string    `json:"title"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	// AllDay events have dates and no times; Start is the day's midnight.
	AllDay bool `json:"allDay,omitempty"`
}

// Job is a reminder still to come today.
type Job struct {
	Name  string    `json:"name"`
	Title string    `json:"title"`
	At    time.Time `json:"at"`
}

// Result is the reply of a job that ran in the night.
type Result struct {
	Session string    `json:"session"`
	Title   string    `json:"title"`
	At      time.Time `json:"at"`
	// Text is the reply's first line.
	Text   string `json:"text"`
	Failed bool   `json:"failed,omitempty"`
}

// Task is a to-do of the list.
type Task struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

// Night is the span of the quiet hours that ended on the morning of the
// day of now: from 23:00 the evening before until 08:00. Jobs' replies
// arrive without a sound then.
func Night(now time.Time) (time.Time, time.Time) {
	y, m, d := now.Date()
	return time.Date(y, m, d-1, NightStart, 0, 0, 0, now.Location()),
		time.Date(y, m, d, NightEnd, 0, 0, 0, now.Location())
}

// The hours the quiet night starts and ends.
const (
	NightStart = 23
	NightEnd   = 8
)

// Quiet says whether t is in the quiet hours.
func Quiet(t time.Time) bool {
	return t.Hour() >= NightStart || t.Hour() < NightEnd
}

// Bounds are the start of the day of now and of the day after it.
func Bounds(now time.Time) (time.Time, time.Time) {
	y, m, d := now.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, now.Location()), time.Date(y, m, d+1, 0, 0, 0, 0, now.Location())
}

// FirstLine is the first line of text that has any, cut to a length a
// card shows.
func FirstLine(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if r := []rune(line); len(r) > 140 {
			return string(r[:139]) + "…"
		}
		return line
	}
	return ""
}

// task matches a to-do line: a list item that starts with a checkbox.
var task = regexp.MustCompile(`^(\s*[-*+]\s+\[)([ xX])(\]\s+)(.*)$`)

// Tasks reads the to-do list. A missing file has none.
func Tasks(cfg config.Config) ([]Task, error) {
	data, err := read(cfg)
	if err != nil {
		return nil, err
	}

	tasks := []Task{}
	for _, line := range lines(string(data)) {
		if m := task.FindStringSubmatch(line); m != nil && strings.TrimSpace(m[4]) != "" {
			tasks = append(tasks, Task{Text: strings.TrimSpace(m[4]), Done: m[2] != " "})
		}
	}
	return tasks, nil
}

// writing holds one Check at a time.
var writing sync.Mutex

// Check ticks the to-do at index, or with done false unticks it, when that
// to-do still reads text, and commits it when it can. A tick names its
// to-do rather than the list's version, so ticks that overlap, from one
// device or two, all land.
func Check(cfg config.Config, index int, text string, done bool) error {
	writing.Lock()
	defer writing.Unlock()

	data, err := read(cfg)
	if err != nil {
		return err
	}

	all := strings.Split(string(data), "\n")
	n, at := 0, -1
	for i, line := range lines(string(data)) {
		m := task.FindStringSubmatch(line)
		if m == nil || strings.TrimSpace(m[4]) == "" {
			continue
		}
		if n == index {
			if strings.TrimSpace(m[4]) != text {
				return ErrChanged
			}
			at = i
			mark := " "
			if done {
				mark = "x"
			}
			all[i] = m[1] + mark + m[3] + m[4]
			break
		}
		n++
	}
	if at < 0 {
		return ErrChanged
	}

	changed := strings.Join(all, "\n")
	if changed == string(data) {
		return nil
	}
	// The agent may have written the list since it was read: its change
	// stays, and the tick is refused. What is left is the moment between
	// this read and the write.
	if now, err := read(cfg); err != nil {
		return err
	} else if string(now) != string(data) {
		return ErrChanged
	}
	if err := proc.WriteFile(cfg, filepath.Join(cfg.Space, File), []byte(changed), 0o644); err != nil {
		return err
	}

	// The tick is saved once written; the agent's next commit takes it when
	// this one fails.
	if err := commit(cfg); err != nil {
		log.Printf("today: %v", err)
	}
	return nil
}

// lines are the file's lines, with those inside code blocks blanked, so an
// example there is no to-do. Their indexes match the file's.
func lines(text string) []string {
	all := strings.Split(text, "\n")
	out := make([]string, len(all))
	fenced := false
	for i, line := range all {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fenced = !fenced
			continue
		}
		if !fenced {
			out[i] = line
		}
	}
	return out
}

func read(cfg config.Config) ([]byte, error) {
	path := filepath.Join(cfg.Space, File)
	// cat fails the same way for a missing file and any other error, so
	// den asks first, which it may do as root.
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return proc.ReadFile(cfg, path)
}

// commit commits the file alone, so what else the agent has changed in the
// space stays for its own commit.
func commit(cfg config.Config) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	script := `cd "$1" && git add -- "$2" && { git diff --cached --quiet -- "$2" || git commit -q -m "Tick a to-do in the app" -- "$2"; }`
	cmd, err := proc.Command(ctx, cfg, "/bin/sh", "-c", script, "commit", cfg.Space, File)
	if err != nil {
		return err
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("committing the list: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
