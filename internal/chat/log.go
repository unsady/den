package chat

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/unsady/den/internal/notify"
)

// shownActions is the most actions /log lists.
const shownActions = 30

// showLog lists what the agent changed outside in the last day and the
// kinds of action the user allowed for good, with a button to forget each,
// in a new message or in place of edit.
func (c *Chat) showLog(ctx context.Context, telegram *notify.Telegram, edit int64) {
	now := time.Now()
	actions := c.den.Actions(now.Add(-24 * time.Hour))

	var b strings.Builder
	b.WriteString("<b>Outside, last 24 hours</b>\n")
	if len(actions) == 0 {
		b.WriteString("The agent changed nothing outside.\n")
	}
	if len(actions) > shownActions {
		fmt.Fprintf(&b, "…%d earlier\n", len(actions)-shownActions)
		actions = actions[len(actions)-shownActions:]
	}
	for _, a := range actions {
		fmt.Fprintf(&b, "%s · %s · %s\n", a.At.Local().Format("15:04"), html.EscapeString(a.What), a.Outcome)
	}

	var rows [][]notify.Button
	rules := c.den.Rules()
	b.WriteString("\n<b>Always allowed</b>\n")
	if len(rules) == 0 {
		b.WriteString("Nothing yet. Press Always on a question to stop being asked about that kind of request.")
	}
	for _, r := range rules {
		fmt.Fprintf(&b, "• %s\n", html.EscapeString(r.Label))
		rows = append(rows, []notify.Button{{Text: "✕ " + clipRunes(r.Label, 40), Data: "rf:" + r.ID()}})
	}

	c.show(ctx, telegram, edit, strings.TrimSpace(b.String()), append(rows, []notify.Button{closeButton}))
}
