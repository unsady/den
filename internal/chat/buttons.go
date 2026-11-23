package chat

import (
	"context"
	"fmt"
	"html"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/unsady/den/internal/approve"
	"github.com/unsady/den/internal/jobs"
	"github.com/unsady/den/internal/notify"
	"github.com/unsady/den/internal/store"
)

// The /sessions and /jobs lists, and the buttons under den's messages.

// closeButton deletes the list it is under.
var closeButton = notify.Button{Text: "✕", Data: "x:"}

// listSessions lists sessions to continue, in a new message or in place of
// edit, the ones used last on top. Job runs are a list of their own, and
// words find sessions of any kind by title. The archive is the apps': the
// list holds archived sessions too.
func (c *Chat) listSessions(ctx context.Context, telegram *notify.Telegram, words string, jobRuns bool, edit int64) {
	query := strings.TrimSpace(words)
	filter := store.Filter{Query: query, Shelf: store.Everywhere, Limit: recentSessions}
	switch {
	case query != "":
	case jobRuns:
		filter.Kinds = []store.Kind{store.KindJob}
	default:
		filter.Kinds = []store.Kind{store.KindChat, store.KindBackground}
	}
	list, err := c.store.Sessions(ctx, filter)
	if err != nil {
		c.send(ctx, telegram, "Could not read the sessions: "+err.Error())
		return
	}
	current := c.cursor(store.Telegram)

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
		text = "Sessions with “" + html.EscapeString(words) + "”:"
	case jobRuns:
		text = "Recent job runs:"
	default:
		text = "Recent sessions:"
	}

	// Each session is one button, so the list is not written out twice;
	// • marks the current one.
	var rows [][]notify.Button
	for _, s := range list {
		label := clipRunes(titleOf(s), 40) + " · " + ago(s.Updated)
		if s.Kind != store.KindChat && !jobRuns {
			label = icons[s.Kind] + " " + label
		}
		rows = append(rows, []notify.Button{{Text: mark(label, s.ID == current), Data: "s:" + s.ID}})
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

// listJobs lists the jobs in a new message, or in place of edit. The agent
// changes, runs, and removes jobs.
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
		text += fmt.Sprintf("\n<b>%s</b> · next %s", html.EscapeString(j.Title), j.NextRun.Format("Mon 2 Jan 15:04"))
		if j.LastRun != nil {
			text += " · last " + j.LastStatus + " " + ago(*j.LastRun)
		}
		if j.Slot == jobs.Second && second {
			text += " · second model"
		}
		text += "\n" + html.EscapeString(clipRunes(j.Prompt, 80)) + "\n"
	}
	text += "\nAsk the agent to change or run a job."
	c.show(ctx, telegram, edit, text, append(rows, []notify.Button{closeButton}))
}

// press acts on a button from /sessions or /jobs.
func (c *Chat) press(ctx context.Context, telegram *notify.Telegram, cb notify.Callback) {
	kind, ref, _ := strings.Cut(cb.Data, ":")
	answer, text := "", ""
	var rows [][]notify.Button

	switch kind {
	case "s":
		answer = "That session no longer exists."
		if s, err := c.store.Session(ctx, ref); err == nil {
			// The list turns into the one line that names the choice.
			c.switchTo(s.ID)
			answer = " "
			line := "↺ " + html.EscapeString(titleOf(s))
			if model := c.modelName(ctx); model != "" {
				line += " · " + model
			}
			c.editHTML(ctx, telegram, cb.Message, line, nil)
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

	case "ok", "no", "al", "hr":
		// The answer to a question den asked, such as whether to send a
		// request. den shrinks the message to the action's one line and
		// what the answer allowed, so answered questions take little room.
		outcome := map[string]string{"ok": approve.Allowed, "no": approve.Denied, "al": approve.Always, "hr": approve.ForHour}[kind]
		answer = "This question has expired."
		if _, found := c.den.Answer(ref, outcome); found {
			answer = " "
		}

	case "rf":
		answer = "That rule is already gone."
		if r, found := c.den.Forget(ref); found {
			answer = "den will ask about " + r.Label + " again"
			c.showLog(ctx, telegram, cb.Message)
		}

	case "rt", "ct":
		// The button goes once pressed, so the turn is not asked twice.
		answer = " "
		var err error
		if kind == "rt" {
			err = c.Retry(ctx, ref)
		} else {
			err = c.Continue(ctx, ref)
		}
		if err != nil {
			answer = err.Error()
		}
		call, cancel := context.WithTimeout(ctx, time.Minute)
		if err := telegram.ClearButtons(call, cb.Message); err != nil {
			log.Printf("chat: buttons: %v", err)
		}
		cancel()

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
