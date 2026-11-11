package chat

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/unsady/den/internal/notify"
)

// What the chat shows while a turn runs: typing, and the latest steps.

func (c *Chat) showTyping(ctx context.Context, done <-chan struct{}) {
	t := time.NewTicker(typing)
	defer t.Stop()

	for {
		if telegram, err := notify.Load(c.cfg); err == nil {
			call, cancel := context.WithTimeout(ctx, typing)
			_ = telegram.Typing(call)
			cancel()
		}
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-t.C:
		}
	}
}

// Note shows text under the running step of the turn's progress message,
// until the next step. It drops the text when no turn is running or an
// earlier note waits.
func (c *Chat) Note(text string) {
	select {
	case c.notes <- clipRunes(strings.TrimSpace(text), 200):
	default:
	}
}

// showProgress keeps one message in the chat that lists the latest steps fx
// reported, and deletes it when done closes, before the reply is sent.
func (c *Chat) showProgress(ctx context.Context, lines <-chan string, done <-chan struct{}) {
	t := time.NewTicker(progressEvery)
	defer t.Stop()

	var (
		recent  []stepGroup
		skipped int
		id      int64
		changed bool
		note    string
	)
	// A note left from a turn that ended belongs to no step of this one.
	select {
	case <-c.notes:
	default:
	}
	for {
		select {
		case note = <-c.notes:
			changed = len(recent) > 0

		case line := <-lines:
			text, ok := step(line)
			if !ok {
				continue
			}
			note = ""
			// The same step again counts up instead of adding a line.
			if n := len(recent); n > 0 && recent[n-1].text == text {
				recent[n-1].count++
			} else {
				recent = append(recent, stepGroup{text: text, count: 1})
			}
			if len(recent) > steps {
				skipped += recent[0].count
				recent = recent[1:]
			}
			changed = true

		case <-t.C:
			if !changed {
				continue
			}
			telegram, err := notify.Load(c.cfg)
			if err != nil {
				continue
			}
			text := progressText(recent, skipped)
			if note != "" {
				text += "\n   " + note
			}
			call, cancel := context.WithTimeout(ctx, typing)
			if id == 0 {
				id, err = telegram.Post(call, text)
			} else {
				err = telegram.Edit(call, id, text)
			}
			cancel()
			if err != nil {
				log.Printf("chat: progress: %v", err)
			}
			changed = false

		case <-done:
			if id == 0 {
				return
			}
			if telegram, err := notify.Load(c.cfg); err == nil {
				call, cancel := context.WithTimeout(ctx, typing)
				if err := telegram.Delete(call, id); err != nil {
					log.Printf("chat: progress: %v", err)
				}
				cancel()
			}
			return
		}
	}
}

func progressText(recent []stepGroup, skipped int) string {
	text := "Working…"
	if skipped > 0 {
		if skipped == 1 {
			text += " (1 earlier step)"
		} else {
			text += fmt.Sprintf(" (%d earlier steps)", skipped)
		}
	}
	// Every step but the last is over; the last runs now.
	for i, g := range recent {
		if i < len(recent)-1 {
			text += "\n• " + g.done().String()
		} else {
			text += "\n→ " + g.String() + "…"
		}
	}
	return text
}
