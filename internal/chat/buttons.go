package chat

import (
	"context"
	"fmt"
	"html"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/unsady/den/internal/approve"
	"github.com/unsady/den/internal/jobs"
	"github.com/unsady/den/internal/notify"
	"github.com/unsady/den/internal/sessions"
)

// The /sessions and /jobs lists, and the buttons under den's messages.

// closeButton deletes the list it is under.
var closeButton = notify.Button{Text: "✕", Data: "x:"}

// listSessions lists sessions to continue, in a new message or in place of
// edit. The chat's own come first, the ones used last on top; job runs are
// a list of their own, and words find sessions of either kind by title.
func (c *Chat) listSessions(ctx context.Context, telegram *notify.Telegram, words string, jobRuns bool, edit int64) {
	all, err := sessions.List(c.cfg.Sessions, 1<<20)
	if err != nil {
		c.send(ctx, telegram, "Could not read the sessions: "+err.Error())
		return
	}
	var ofJobs map[string]bool
	if c.den != nil {
		ofJobs = c.den.JobSessions()
	}

	c.mu.Lock()
	current := c.state.Session
	rank := map[string]int{}
	for i, id := range c.state.Opened {
		rank[id] = i + 1
	}
	c.mu.Unlock()

	var list []sessions.Summary
	query := strings.ToLower(strings.TrimSpace(words))
	for _, s := range all {
		switch {
		case query != "":
			if !strings.Contains(strings.ToLower(s.Title), query) {
				continue
			}
		case ofJobs[s.ID] != jobRuns:
			continue
		}
		list = append(list, s)
	}
	if query == "" && !jobRuns {
		// sessions.List is newest first; the chat's own go before the rest.
		sort.SliceStable(list, func(a, b int) bool {
			ra, rb := rank[list[a].ID], rank[list[b].ID]
			return ra != 0 && (rb == 0 || ra < rb)
		})
	}
	if len(list) > recentSessions {
		list = list[:recentSessions]
	}

	var text string
	switch {
	case query != "" && len(list) == 0:
		c.send(ctx, telegram, "No session title has “"+words+"”.")
		return
	case len(list) == 0 && jobRuns:
		text = "No job runs yet."
	case len(list) == 0:
		c.send(ctx, telegram, "No sessions yet. Write to start one.")
		return
	case query != "":
		text = "Sessions with “" + html.EscapeString(words) + "”. Pick one to continue it:\n"
	case jobRuns:
		text = "Recent job runs. Pick one to continue it:\n"
	default:
		text = "Recent sessions. Pick one to continue it:\n"
	}

	var rows [][]notify.Button
	for i, s := range list {
		mark := ""
		if ofJobs[s.ID] && !jobRuns {
			mark = "⏰ "
		}
		line := fmt.Sprintf("\n%d. %s%s, %s", i+1, mark, html.EscapeString(s.Title), ago(s.Updated))
		if s.ID == current {
			line += "  ← current"
		}
		text += line
		rows = append(rows, []notify.Button{{Text: fmt.Sprintf("%d. %s", i+1, clipRunes(s.Title, 40)), Data: "s:" + s.ID}})
	}

	last := []notify.Button{{Text: "⏰ Job runs", Data: "sj:"}, closeButton}
	switch {
	case query != "":
		last = []notify.Button{closeButton}
	case jobRuns:
		last = []notify.Button{{Text: "← Chats", Data: "sc:"}, closeButton}
	}
	c.show(ctx, telegram, edit, text, append(rows, last))
}

// listJobs lists the jobs in a new message, or in place of edit. A job can
// move between the main and the second model once there is a second one.
func (c *Chat) listJobs(ctx context.Context, telegram *notify.Telegram, edit int64) {
	list := c.den.ListJobs()
	if len(list) == 0 {
		c.send(ctx, telegram, "No jobs. Ask the agent to schedule one.")
		return
	}
	second := c.second() != nil

	text := "Jobs:\n"
	var rows [][]notify.Button
	for _, j := range list {
		last := "never ran"
		if j.LastRun != nil {
			last = "last " + j.LastStatus + " " + ago(*j.LastRun)
		}
		text += fmt.Sprintf("\n<b>%s</b>: %s, next %s, %s", html.EscapeString(j.Label()), html.EscapeString(j.Schedule), j.NextRun.Format("Mon 2 Jan 15:04"), last)
		if j.Slot == jobs.Second {
			text += ", on the second model"
		}
		text += "\n" + html.EscapeString(clipRunes(j.Prompt, 100)) + "\n"

		row := []notify.Button{
			{Text: "Run " + clipRunes(j.Label(), 20), Data: "jr:" + j.ID},
			{Text: "Remove", Data: "jd:" + j.ID},
		}
		switch {
		case j.Slot == jobs.Second:
			row = append(row, notify.Button{Text: "→ main", Data: "jm:" + j.ID})
		case second:
			row = append(row, notify.Button{Text: "→ second", Data: "js:" + j.ID})
		}
		rows = append(rows, row)
	}
	c.show(ctx, telegram, edit, text, append(rows, []notify.Button{closeButton}))
}

// press acts on a button from /sessions or /jobs.
func (c *Chat) press(ctx context.Context, telegram *notify.Telegram, cb notify.Callback) {
	kind, ref, _ := strings.Cut(cb.Data, ":")
	answer, text := "", ""
	var rows [][]notify.Button

	switch kind {
	case "s":
		list, _ := sessions.List(c.cfg.Sessions, 1<<20)
		answer = "That session no longer exists."
		for _, s := range list {
			if s.ID == ref {
				// The list turns into the one line that names the choice.
				c.switchTo(s.ID)
				answer = " "
				line := "↺ " + html.EscapeString(s.Title)
				if model := c.modelName(ctx); model != "" {
					line += " · " + model
				}
				c.editHTML(ctx, telegram, cb.Message, line, nil)
			}
		}

	case "sj", "sc":
		answer = " "
		c.listSessions(ctx, telegram, "", kind == "sj", cb.Message)

	case "m":
		// The list turns into the one line that names the choice.
		answer = " "
		provider, model, _ := strings.Cut(ref, "/")
		if chosen, err := c.chooseModel(ctx, provider, model); err != nil {
			answer = err.Error()
		} else {
			c.editHTML(ctx, telegram, cb.Message, chosen, nil)
		}

	case "e":
		answer = " "
		if chosen, err := c.chooseEffort(ctx, ref); err != nil {
			answer = err.Error()
		} else {
			c.editHTML(ctx, telegram, cb.Message, chosen, nil)
		}

	case "r":
		answer = " "
		c.recentModels(ctx, telegram, cb.Message)

	case "a", "A":
		answer = " "
		provider, page, _ := strings.Cut(ref, ":")
		n, _ := strconv.Atoi(page)
		p := mainPicker
		if kind == "A" {
			p = secondPicker
		}
		c.allModels(ctx, telegram, cb.Message, p, provider, n)

	case "2":
		answer = " "
		c.secondModels(ctx, telegram, cb.Message)

	case "n":
		// Like the main model's list, this one turns into the line that
		// names the choice, with the new model's effort levels if any.
		answer = " "
		provider, model, _ := strings.Cut(ref, "/")
		if chosen, levels, err := c.chooseSecond(ctx, provider, model); err != nil {
			answer = err.Error()
		} else {
			c.editHTML(ctx, telegram, cb.Message, chosen, levels)
		}

	case "f":
		answer = " "
		if chosen, err := c.chooseSecondEffort(ctx, ref); err != nil {
			answer = err.Error()
		} else {
			c.editHTML(ctx, telegram, cb.Message, chosen, nil)
		}

	case "js", "jm":
		slot := jobs.Second
		if kind == "jm" {
			slot = jobs.Main
		}
		if j, err := c.den.SetJobSlot(ref, slot); err != nil {
			answer = err.Error()
		} else {
			answer = j.Label() + " runs on the " + slot + " model"
			c.listJobs(ctx, telegram, cb.Message)
		}

	case "ok", "no", "al", "hr":
		// The answer to a question den asked, such as whether to send a
		// request. The message shrinks to the action's one line and the
		// answer, so answered questions take little room.
		outcome, result := approve.Allowed, "\n✓ Allowed"
		switch kind {
		case "no":
			outcome, result = approve.Denied, "\n✗ Denied"
		case "al":
			outcome, result = approve.Always, "\n✓ Always allowed. /log lists what you always allow."
		case "hr":
			outcome, result = approve.ForHour, "\n✓ Allowed, with the same kind for an hour"
		}
		answer = "This question has expired."
		if question, found := c.den.Answer(ref, outcome); found {
			answer = " "
			c.editPlain(ctx, telegram, cb.Message, question+result)
		}

	case "rf":
		answer = "That rule is already gone."
		if r, found := c.den.Forget(ref); found {
			answer = "den will ask about " + r.Label + " again"
			c.showLog(ctx, telegram, cb.Message)
		}

	case "jr":
		if j, err := c.den.StartJob(ref); err != nil {
			answer = err.Error()
		} else {
			answer, text = "Started", "Started job "+j.Label()+". Its reply comes when it finishes."
		}

	case "jd":
		answer = "That job no longer exists."
		for _, j := range c.den.ListJobs() {
			if j.ID == ref {
				answer = " "
				text = "Remove job " + j.Label() + "?\n" + clipRunes(j.Prompt, 200)
				rows = [][]notify.Button{{{Text: "Remove", Data: "jx:" + j.ID}, {Text: "Keep", Data: "k:"}}}
			}
		}

	case "jx":
		if j, err := c.den.RemoveJob(ref); err != nil {
			answer = err.Error()
		} else {
			answer, text = "Removed", "Removed job "+j.Label()+"."
		}

	case "k":
		answer = "Kept"

	case "x":
		// The list goes away, so it does not stay in the chat.
		answer = " "
		call, cancel := context.WithTimeout(ctx, time.Minute)
		if err := telegram.Delete(call, cb.Message); err != nil {
			log.Printf("chat: close: %v", err)
		}
		cancel()

	default:
		answer = "This button is out of date."
	}

	call, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := telegram.Answer(call, cb.ID, strings.TrimSpace(answer)); err != nil {
		log.Printf("chat: answer: %v", err)
	}
	switch {
	case len(rows) > 0:
		c.sendButtons(ctx, telegram, text, rows)
	case text != "":
		c.send(ctx, telegram, text)
	}
}
