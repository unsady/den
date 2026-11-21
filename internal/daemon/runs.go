package daemon

import (
	"context"
	"fmt"
	"log"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/unsady/den/internal/events"
	"github.com/unsady/den/internal/notify"
)

// What den shows while den map runs in its background: in the app, a line
// in the session that started the runs; in Telegram, one message with a
// line for each run whose session's replies go there, which den deletes
// once none is left.

// runsEvery is how often den edits the message, so Telegram does not
// refuse the edits as too many.
var runsEvery = 10 * time.Second

// mapRun is one den map run in den's background, as its last progress
// report says.
type mapRun struct {
	// session is the session whose agent started the run.
	session string
	label   string
	started time.Time
	done    int
	total   int
	failed  int
}

// runStarted adds a run to the message, and starts showing the message
// when it is the first.
func (d *daemon) runStarted(run, session, label string, total int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.maps[run] = &mapRun{session: session, label: label, started: time.Now(), total: total}
	d.mapsChanged = true
	if !d.mapsShown {
		d.mapsShown = true
		d.wg.Add(1)
		go d.showRuns()
	}
}

// runProgress keeps how far a run is. It returns false for a run den does
// not know.
func (d *daemon) runProgress(run string, done, total, failed int) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, ok := d.maps[run]
	if ok {
		r.done, r.total, r.failed = done, total, failed
		d.mapsChanged = true
	}
	return ok
}

// runEnded takes a run off the message.
func (d *daemon) runEnded(run string) {
	d.mu.Lock()
	delete(d.maps, run)
	d.mapsChanged = true
	d.mu.Unlock()
}

// showRuns keeps the app's lines and the Telegram message up to date while
// runs go on, and clears them when none is left.
func (d *daemon) showRuns() {
	defer d.wg.Done()
	t := time.NewTicker(runsEvery)
	defer t.Stop()

	var id int64
	shown := map[string]string{}
	for {
		d.mu.Lock()
		// Copies, since the runs' progress changes under d.mu.
		runs := map[string]*mapRun{}
		for k, r := range d.maps {
			c := *r
			runs[k] = &c
		}
		changed, left := d.mapsChanged, len(d.maps)
		d.mapsChanged = false
		// A run that starts after this shows in a message of its own.
		d.mapsShown = left > 0
		d.mu.Unlock()

		if changed || left == 0 {
			lines := sessionRuns(runs)
			for session := range shown {
				if _, ok := lines[session]; !ok {
					d.events.Publish(events.Event{Type: events.Run, Session: session})
				}
			}
			for session, text := range lines {
				if shown[session] != text {
					d.events.Publish(events.Event{Type: events.Run, Session: session, Text: text})
				}
			}
			shown = lines
			id = d.showRunsIn(id, d.telegramRuns(runs))
		}
		if left == 0 {
			return
		}

		select {
		case <-d.ctx.Done():
			d.mu.Lock()
			d.mapsShown = false
			d.mu.Unlock()
			return
		case <-t.C:
		}
	}
}

// telegramRuns keeps the runs whose sessions' replies go to Telegram.
func (d *daemon) telegramRuns(runs map[string]*mapRun) map[string]*mapRun {
	maps.DeleteFunc(runs, func(_ string, r *mapRun) bool { return !d.chat.ToTelegram(r.session) })
	return runs
}

// showRunsIn puts the runs in Telegram's message id, posting it when there
// is none and deleting it when no run is left. It returns the message's id,
// 0 once there is none.
func (d *daemon) showRunsIn(id int64, runs map[string]*mapRun) int64 {
	telegram, err := notify.Load(d.cfg)
	if err != nil {
		return id
	}

	ctx, cancel := context.WithTimeout(d.ctx, time.Minute)
	defer cancel()
	switch {
	case len(runs) == 0 && id != 0:
		err = telegram.Delete(ctx, id)
		id = 0
	case len(runs) == 0:
	case id == 0:
		id, err = telegram.Post(ctx, runsText(runs))
	default:
		err = telegram.Edit(ctx, id, runsText(runs))
	}
	if err != nil {
		log.Printf("map runs: %v", err)
	}
	return id
}

// sessionRuns gives each session that started runs its line for the app,
// the runs one after another, the oldest first.
func sessionRuns(runs map[string]*mapRun) map[string]string {
	lines := map[string]string{}
	for _, r := range sorted(runs) {
		if r.session == "" {
			continue
		}
		if lines[r.session] != "" {
			lines[r.session] += "; "
		}
		lines[r.session] += "den map · " + r.line()
	}
	return lines
}

// runsText lists the runs, the oldest first.
func runsText(runs map[string]*mapRun) string {
	lines := []string{"🗂 den map"}
	for _, r := range sorted(runs) {
		lines = append(lines, r.line())
	}
	return strings.Join(lines, "\n")
}

// sorted lists the runs, the oldest first.
func sorted(runs map[string]*mapRun) []*mapRun {
	list := slices.Collect(maps.Values(runs))
	slices.SortFunc(list, func(a, b *mapRun) int { return a.started.Compare(b.started) })
	return list
}

// line says how far r is: "deepseek-v4.1-flash · 38 of 54 · 1 failed".
func (r *mapRun) line() string {
	line := fmt.Sprintf("%s · %d of %d", r.label, r.done, r.total)
	if r.failed > 0 {
		line += fmt.Sprintf(" · %d failed", r.failed)
	}
	return line
}
