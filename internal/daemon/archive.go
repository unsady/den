package daemon

import "log"

// archiveIdle puts the sessions nobody used for a while in the archive,
// except those with a question that waits for the user.
func (d *daemon) archiveIdle() {
	var waiting []string
	for _, q := range d.ask.Pending() {
		if q.Session != "" {
			waiting = append(waiting, q.Session)
		}
	}

	if err := d.chat.ArchiveIdle(d.ctx, waiting); err != nil {
		log.Printf("archive idle sessions: %v", err)
	}
}
