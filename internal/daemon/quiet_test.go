package daemon

import (
	"testing"

	"github.com/unsady/den/internal/jobs"
)

func TestSilent(t *testing.T) {
	job := jobs.Job{Name: "news"}
	run := jobs.Run{Job: "news", Output: "Three new posts."}

	if silent(job, run, false) {
		t.Error("a reply with text was silent")
	}
	if !silent(job, run, true) {
		t.Error("a quiet run had something to send")
	}
	if !silent(job, jobs.Run{Job: "news"}, false) {
		t.Error("an empty reply had something to send")
	}
	if !silent(jobs.Job{Name: "news", Silent: true}, run, false) {
		t.Error("a silent job had something to send")
	}

	// A failure always reaches the user.
	if silent(job, jobs.Run{Job: "news", Error: "timed out"}, true) {
		t.Error("a failed quiet run was silent")
	}
}

func TestDeliverQuiet(t *testing.T) {
	// A quiet run sends nothing, so deliver needs neither Telegram nor the
	// chat.
	d := &daemon{}
	run := jobs.Run{Job: "news", Output: "Three new posts."}

	if d.deliver(jobs.Job{Name: "news", Title: "News"}, run, true, nil) {
		t.Error("a quiet run was delivered")
	}
}
