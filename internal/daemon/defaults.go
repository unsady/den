package daemon

import (
	"context"
	"strings"
	"time"

	"github.com/unsady/den/internal/jobs"
	"github.com/unsady/den/internal/store"
)

// defaultsKey lists den's own jobs that den added once, so an update brings
// a new one and a job the user removed stays removed.
const defaultsKey = "jobs.defaults"

// addDefaultJobs adds each of defaultJobs once.
func addDefaultJobs(ctx context.Context, db *store.Store, js *jobs.Store) error {
	value, err := db.Setting(ctx, defaultsKey)
	if err != nil {
		return err
	}
	added := map[string]bool{}
	for _, name := range strings.Fields(value) {
		added[name] = true
	}

	var names []string
	for _, j := range defaultJobs {
		names = append(names, j.Name)
		if added[j.Name] {
			continue
		}
		if _, err := js.Get(j.Name); err == nil {
			// The user has a job of their own by this name.
			continue
		}
		if _, err := js.Add(j, time.Now()); err != nil {
			return err
		}
	}
	return db.SetSetting(ctx, defaultsKey, strings.Join(names, " "))
}
