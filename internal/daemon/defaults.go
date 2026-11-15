package daemon

import (
	"errors"
	"os"
	"strings"
	"time"

	"github.com/unsady/den/internal/jobs"
)

// addDefaultJobs adds each of defaultJobs once, and lists the ones it added
// at path: an update brings a new one, and a job the user removed stays
// removed.
func addDefaultJobs(store *jobs.Store, path string, created bool) error {
	added := map[string]bool{}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		for _, name := range strings.Fields(string(data)) {
			added[name] = true
		}
	case !errors.Is(err, os.ErrNotExist):
		return err
	case !created:
		// den added reflect with the store before it kept this list.
		added["reflect"] = true
	}

	var names []string
	for _, j := range defaultJobs {
		names = append(names, j.Name)
		if added[j.Name] {
			continue
		}
		if _, err := store.Get(j.Name); err == nil {
			// The user has a job of their own by this name.
			continue
		}
		if _, err := store.Add(j, time.Now()); err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(strings.Join(names, "\n")+"\n"), 0o644)
}
