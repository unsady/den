package daemon

import (
	"log"
	"time"

	"github.com/unsady/den/internal/today"
)

// dayKey keeps the day den last saw, so a day that began while den was
// down still clears the list once den is back.
const dayKey = "today.day"

// newDay clears the past day's to-dos once a new day begins. The first day
// den sees clears nothing: it cannot tell the list's day.
func (d *daemon) newDay(now time.Time) {
	date := now.Format(time.DateOnly)
	seen, err := d.db.Setting(d.ctx, dayKey)
	if err != nil {
		log.Printf("new day: %v", err)
		return
	}
	if seen == date {
		return
	}

	if seen != "" {
		if err := today.Clear(d.cfg); err != nil {
			log.Printf("new day: %v", err)
			return
		}
	}
	if err := d.db.SetSetting(d.ctx, dayKey, date); err != nil {
		log.Printf("new day: %v", err)
	}
}
