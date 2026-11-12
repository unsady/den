package daemon

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/unsady/den/internal/notify"
)

// What the chat shows while den map runs in den's background: one message
// with a line for each run, which den deletes once the last run ends.

// runsEvery is how often den edits the message, so Telegram does not
// refuse the edits as too many.
var runsEvery = 10 * time.Second

// mapRun is one den map run in den's background, as its last progress
// report says.
type mapRun struct {
	label   string
	started time.Time
	done    int
	total   int
	failed  int
}

// runStarted adds a run to the message, and starts showing the message
// when it is the first.
func (d *daemon) runStarted(run, label string, total int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.maps[run] = &mapRun{label: label, started: time.Now(), total: total}
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

// showRuns keeps the message up to date while runs go on, and deletes it
// when none is left.
func (d *daemon) showRuns() {
	defer d.wg.Done()
	t := time.NewTicker(runsEvery)
	defer t.Stop()

	var id int64
	for {
		d.mu.Lock()
		text, changed, left := runsText(d.maps), d.mapsChanged, len(d.maps)
		d.mapsChanged = false
		// A run that starts after this shows in a message of its own.
		d.mapsShown = left > 0
		d.mu.Unlock()

		telegram, err := notify.Load(d.cfg)
		if err == nil && (changed || left == 0) {
			ctx, cancel := context.WithTimeout(d.ctx, time.Minute)
			switch {
			case left == 0 && id != 0:
				err = telegram.Delete(ctx, id)
			case left == 0:
			case id == 0:
				id, err = telegram.Post(ctx, text)
			default:
				err = telegram.Edit(ctx, id, text)
			}
			cancel()
			if err != nil {
				log.Printf("map runs: %v", err)
			}
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

// runsText lists the runs, the oldest first.
func runsText(runs map[string]*mapRun) string {
	list := make([]*mapRun, 0, len(runs))
	for _, r := range runs {
		list = append(list, r)
	}
	slices.SortFunc(list, func(a, b *mapRun) int { return a.started.Compare(b.started) })

	lines := []string{"🗂 Running"}
	for _, r := range list {
		line := fmt.Sprintf("%s · %d of %d", r.label, r.done, r.total)
		if r.failed > 0 {
			line += fmt.Sprintf(" · %d failed", r.failed)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
