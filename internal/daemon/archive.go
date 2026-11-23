package daemon

import "log"

// archiveIdle puts the sessions nobody used for a while in the archive,
// except those with a question that waits for the user or a den map run.
func (d *daemon) archiveIdle() {
	var waiting []string
	for _, q := range d.ask.Pending() {
		if q.Session != "" {
			waiting = append(waiting, q.Session)
		}
	}
	d.mu.Lock()
	for _, run := range d.maps {
		if run.session != "" {
			waiting = append(waiting, run.session)
		}
	}
	d.mu.Unlock()

	if err := d.chat.ArchiveIdle(d.ctx, waiting); err != nil {
		log.Printf("archive idle sessions: %v", err)
	}
}
