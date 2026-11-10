package daemon

import (
	"testing"

	"github.com/unsady/den/internal/jobs"
)

func TestHeading(t *testing.T) {
	cases := []struct {
		job  jobs.Job
		want string
	}{
		{jobs.Job{ID: "66202d", Name: "reflect"}, "🌙 Reflect"},
		{jobs.Job{ID: "e8fe13", Name: "morning"}, "⏰ morning"},
		{jobs.Job{ID: "a1b2c3", Once: true}, "⏰ Reminder"},
		{jobs.Job{ID: "a1b2c3"}, "⏰ Scheduled job a1b2c3"},
	}
	for _, c := range cases {
		if got := heading(c.job); got != c.want {
			t.Errorf("heading(%+v) = %q, want %q", c.job, got, c.want)
		}
	}
}
