// Package today keeps the user's day for the app's Today view: the to-dos
// the agent writes in the space's today.md, some at a time of the day,
// which the user ticks off in the app.
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
	Date  string `json:"date"`
	Tasks []Task `json:"tasks"`
}

// Task is a to-do of the list.
type Task struct {
	// Time is when in the day it is, such as 15:00 or 15:00–16:00, for an
	// item that starts with one.
	Time string `json:"time,omitempty"`
	Text string `json:"text"`
	Done bool   `json:"done"`
}

// Read reads the day of now.
func Read(cfg config.Config, now time.Time) (Day, error) {
	tasks, err := Tasks(cfg)
	if err != nil {
		return Day{}, err
	}
	return Day{Date: now.Format(time.DateOnly), Tasks: tasks}, nil
}

// task matches a to-do line: a list item that starts with a checkbox.
var task = regexp.MustCompile(`^(\s*[-*+]\s+\[)([ xX])(\]\s+)(.*)$`)

// clock matches the time an item starts with, such as 15:00 or 9:30, or
// the span, such as 15:00–16:00.
var clock = regexp.MustCompile(`^([01]?\d|2[0-3]):([0-5]\d)((?:\s*[-–]\s*\d{1,2}:\d{2})?)[\s,]+(\S.*)$`)

// parse is the to-do of an item's text after its checkbox.
func parse(text string, done bool) Task {
	text = strings.TrimSpace(text)
	if m := clock.FindStringSubmatch(text); m != nil {
		hour := m[1]
		if len(hour) == 1 {
			hour = "0" + hour
		}
		at := hour + ":" + m[2]
		if end := strings.TrimLeft(m[3], "-– \t"); end != "" {
			at += "–" + end
		}
		return Task{Time: at, Text: m[4], Done: done}
	}
	return Task{Text: text, Done: done}
}

// Tasks reads the to-do list. A missing file has none.
func Tasks(cfg config.Config) ([]Task, error) {
	data, err := read(cfg)
	if err != nil {
		return nil, err
	}

	tasks := []Task{}
	for _, line := range lines(string(data)) {
		if m := task.FindStringSubmatch(line); m != nil && strings.TrimSpace(m[4]) != "" {
			tasks = append(tasks, parse(m[4], m[2] != " "))
		}
	}
	return tasks, nil
}

// writing holds one Check at a time.
var writing sync.Mutex

// Check ticks the to-do at index, or with done false unticks it, when that
// to-do still reads text, its time aside, and commits it when it can. A tick names its
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
			if parse(m[4], false).Text != text {
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
	if err := commit(cfg, "Tick a to-do in the app"); err != nil {
		log.Printf("today: %v", err)
	}
	return nil
}

// Clear drops the past day's ticked to-dos and those at a time, which were
// that day's, and commits the list when it can. The other open to-dos stay
// for the new day.
func Clear(cfg config.Config) error {
	writing.Lock()
	defer writing.Unlock()

	data, err := read(cfg)
	if err != nil || data == nil {
		return err
	}

	all := strings.Split(string(data), "\n")
	var kept []string
	for i, line := range lines(string(data)) {
		if m := task.FindStringSubmatch(line); m != nil {
			if t := parse(m[4], m[2] != " "); t.Done || t.Time != "" {
				continue
			}
		}
		kept = append(kept, all[i])
	}
	if len(kept) == len(all) {
		return nil
	}
	if err := proc.WriteFile(cfg, filepath.Join(cfg.Space, File), []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		return err
	}
	if err := commit(cfg, "Clear the past day's to-dos"); err != nil {
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
func commit(cfg config.Config, message string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	script := `cd "$1" && git add -- "$2" && { git diff --cached --quiet -- "$2" || git commit -q -m "$3" -- "$2"; }`
	cmd, err := proc.Command(ctx, cfg, "/bin/sh", "-c", script, "commit", cfg.Space, File, message)
	if err != nil {
		return err
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("committing the list: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
