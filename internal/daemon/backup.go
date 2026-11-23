package daemon

import (
	"log"
	"time"

	"github.com/unsady/den/internal/backup"
)

// backups takes a snapshot of den's state every backup.Every, once
// backups are set up, and tells the user when one fails and when they
// work again.
func (d *daemon) backups() {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()

	failing := false
	for {
		if backup.On(d.cfg) && time.Since(backup.Last(d.cfg)) >= backup.Every {
			failing = d.snapshot(failing)
		}
		select {
		case <-d.stopping:
			return
		case <-t.C:
		}
	}
}

// snapshot takes one and reports whether it failed, notifying the user
// when that changed since the last one.
func (d *daemon) snapshot(failing bool) bool {
	name, err := backup.Snapshot(d.ctx, d.cfg, time.Now())
	switch {
	case err != nil:
		log.Printf("backup: %v", err)
		if !failing {
			d.notify("", "💾 den could not back up its state: "+err.Error())
		}
		return true
	case failing:
		d.notify("", "💾 Backups work again")
	}
	log.Printf("backup: %s", name)
	return false
}
