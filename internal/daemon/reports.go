package daemon

import (
	"strings"
	"sync"

	"github.com/unsady/den/internal/approve"
)

// reports hold what came of a session's actions while others it asked the
// user about still wait, so its agent hears of them together in one turn
// and answers once, rather than once for each button the user pressed.
type reports struct {
	mu sync.Mutex
	// open counts each session's questions that wait for the user or
	// whose action still runs.
	open map[string]int
	held map[string][]report
}

type report struct{ agent, user string }

// later asks q like Asker.Later and keeps it open for its session until
// then returned, which reports what came of it.
func (d *daemon) later(q approve.Question, then func(error)) (bool, error) {
	d.reports.add(q.Session)
	waiting, err := d.ask.Later(d.ctx, q, func(answer error) {
		then(answer)
		d.settle(q.Session)
	})
	if err != nil || !waiting {
		d.settle(q.Session)
	}
	return waiting, err
}

func (r *reports) add(session string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.open[session]++
}

// keep holds a report while session has questions open, and says whether
// it did.
func (r *reports) keep(session, agent, user string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.open[session] == 0 {
		return false
	}
	r.held[session] = append(r.held[session], report{agent, user})
	return true
}

// done closes one of session's questions and returns its held reports
// once none is left open.
func (r *reports) done(session string) []report {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.open[session]--
	if r.open[session] > 0 {
		return nil
	}
	delete(r.open, session)
	held := r.held[session]
	delete(r.held, session)
	return held
}

// settle closes one of session's questions and, once none is left open,
// tells the agent all that came of them in one turn.
func (d *daemon) settle(session string) {
	held := d.reports.done(session)
	if len(held) == 0 {
		return
	}

	var agent, user []string
	for _, h := range held {
		agent = append(agent, h.agent)
		if h.user != "" {
			user = append(user, h.user)
		}
	}
	d.tell(session, strings.Join(agent, "\n\n"), strings.Join(user, "\n"))
}
