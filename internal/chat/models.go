package chat

import (
	"context"
	"fmt"
	"html"
	"log"
	"slices"
	"strings"

	"github.com/unsady/den/internal/notify"
	"github.com/unsady/den/internal/runner"
)

const (
	// recentModels is how many models /model offers first.
	recentModels = 5
	// modelPage is how many models one page of the full list shows.
	modelPage = 8
	// foundModels is the most models a search offers.
	foundModels = 12
)

// current reads the provider and model the next turn uses.
func (c *Chat) current(ctx context.Context) (runner.Setup, string, string, error) {
	c.mu.Lock()
	provider, model := c.state.Provider, c.state.Model
	c.mu.Unlock()

	setup, err := runner.Status(ctx, c.cfg, provider)
	if err != nil {
		return setup, "", "", err
	}
	if provider == "" {
		provider = setup.Provider
	}
	if model == "" {
		model = setup.Model
	}
	return setup, provider, model, nil
}

// recentModels offers the current model and the ones chosen last, so a
// switch usually takes one press, in a new message or in place of edit.
func (c *Chat) recentModels(ctx context.Context, telegram *notify.Telegram, edit int64) {
	setup, provider, model, err := c.current(ctx)
	if err != nil {
		c.send(ctx, telegram, "Could not read the models: "+err.Error())
		return
	}

	rows := c.recentRows(setup, provider+"/"+model, provider+"/"+model, mainPicker)
	rows = append(rows,
		[]notify.Button{{Text: "Second model →", Data: "2:"}},
		[]notify.Button{{Text: "All models →", Data: "a:" + provider + ":0"}, closeButton},
	)

	text := "⚙︎ " + boldModel(model, setup) + "\n" + c.secondLine(setup) + "\n\nOr find one by name: /model deep"
	c.show(ctx, telegram, edit, text, rows)
}

// secondModels offers the second model, which jobs can run on, from the
// recent ones.
func (c *Chat) secondModels(ctx context.Context, telegram *notify.Telegram, edit int64) {
	setup, provider, model, err := c.current(ctx)
	if err != nil {
		c.send(ctx, telegram, "Could not read the models: "+err.Error())
		return
	}

	second := c.second()
	current := ""
	if second != nil {
		current = second.Provider + "/" + second.Model
	}

	rows := c.recentRows(setup, provider+"/"+model, current, secondPicker)
	rows = append(rows,
		[]notify.Button{{Text: mark("None", second == nil), Data: "n:"}},
		[]notify.Button{{Text: "← Main", Data: "r:"}, {Text: "All models →", Data: "A:" + provider + ":0"}, closeButton},
	)

	text := "⚙︎ " + c.secondLine(setup) + "\n\nJobs set to the second model run on it, so a change here reaches them all."
	c.show(ctx, telegram, edit, text, rows)
}

// picker is what a list of models chooses: the main model or the second.
type picker struct {
	// choose, all, and back start the data of the buttons that choose a
	// model, open the full list, and go back to the recent models.
	choose, all, back string
}

var (
	mainPicker   = picker{choose: "m:", all: "a:", back: "r:"}
	secondPicker = picker{choose: "n:", all: "A:", back: "2:"}
)

// recentRows offers the model first names and the ones chosen last, and
// marks the one current names.
func (c *Chat) recentRows(setup runner.Setup, first, current string, p picker) [][]notify.Button {
	c.mu.Lock()
	refs := append([]string{first}, c.state.Recent...)
	c.mu.Unlock()

	var rows [][]notify.Button
	var seen []string
	for _, ref := range refs {
		if slices.Contains(seen, ref) || len(seen) == recentModels {
			continue
		}
		seen = append(seen, ref)
		provider, model, _ := strings.Cut(ref, "/")
		if b, ok := modelButton(p, provider, model, len(setup.Providers) > 1, ref == current); ok {
			rows = append(rows, []notify.Button{b})
		}
	}
	return rows
}

// allModels offers one page of a provider's models, with the signed-in
// providers above them.
func (c *Chat) allModels(ctx context.Context, telegram *notify.Telegram, edit int64, p picker, provider string, page int) {
	setup, current, model, err := c.current(ctx)
	if err != nil {
		c.send(ctx, telegram, "Could not read the models: "+err.Error())
		return
	}
	if !slices.Contains(setup.Providers, provider) {
		provider = setup.Provider
	}
	models, err := runner.Models(ctx, c.cfg, provider)
	if err != nil {
		c.send(ctx, telegram, "Could not read the models: "+err.Error())
		return
	}

	head := "⚙︎ " + boldModel(model, setup)
	if p == secondPicker {
		head = c.secondLine(setup)
		current, model = "", ""
		if second := c.second(); second != nil {
			current, model = second.Provider, second.Model
		}
	}

	var rows [][]notify.Button
	if len(setup.Providers) > 1 {
		var row []notify.Button
		for _, name := range setup.Providers {
			row = append(row, notify.Button{Text: mark(name, name == provider), Data: p.all + name + ":0"})
		}
		rows = append(rows, row)
	}

	pages := max(1, (len(models)+modelPage-1)/modelPage)
	page = min(max(page, 0), pages-1)
	var row []notify.Button
	for _, m := range models[page*modelPage : min(len(models), (page+1)*modelPage)] {
		if b, ok := modelButton(p, provider, m, false, provider == current && m == model); ok {
			row = append(row, b)
		}
		if len(row) == 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if row != nil {
		rows = append(rows, row)
	}

	nav := []notify.Button{{Text: "← Recent", Data: p.back}, closeButton}
	if pages > 1 {
		data := func(n int) string { return fmt.Sprintf("%s%s:%d", p.all, provider, (n+pages)%pages) }
		nav = append(nav,
			notify.Button{Text: "◀", Data: data(page - 1)},
			notify.Button{Text: fmt.Sprintf("%d/%d", page+1, pages), Data: data(page)},
			notify.Button{Text: "▶", Data: data(page + 1)},
		)
	}
	rows = append(rows, nav)

	text := fmt.Sprintf("%s\n\n%s has %d models.", head, html.EscapeString(provider), len(models))
	c.show(ctx, telegram, edit, text, rows)
}

// findModel chooses the model the user named, or offers the ones whose
// names hold every word they wrote.
func (c *Chat) findModel(ctx context.Context, telegram *notify.Telegram, query string) {
	if query == "default" {
		text, err := c.chooseModel(ctx, "", "")
		c.reply(ctx, telegram, text, err)
		return
	}

	setup, provider, _, err := c.current(ctx)
	if err != nil {
		c.send(ctx, telegram, "Could not read the models: "+err.Error())
		return
	}
	// The current provider goes first, so its model wins a tie.
	providers := append([]string{provider}, setup.Providers...)

	words := strings.Fields(strings.ToLower(query))
	var found []string
	for i, p := range providers {
		if slices.Index(providers, p) != i {
			continue
		}
		models, err := runner.Models(ctx, c.cfg, p)
		if err != nil {
			continue
		}
		for _, m := range models {
			if m == query {
				text, err := c.chooseModel(ctx, p, m)
				c.reply(ctx, telegram, text, err)
				return
			}
			name := strings.ToLower(p + "/" + m)
			if !slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(name, w) }) {
				found = append(found, p+"/"+m)
			}
		}
	}

	switch len(found) {
	case 0:
		c.send(ctx, telegram, fmt.Sprintf("No model matches «%s». Send /model to see them all.", query))
		return
	case 1:
		p, m, _ := strings.Cut(found[0], "/")
		text, err := c.chooseModel(ctx, p, m)
		c.reply(ctx, telegram, text, err)
		return
	}

	var rows [][]notify.Button
	for _, ref := range found[:min(len(found), foundModels)] {
		p, m, _ := strings.Cut(ref, "/")
		if b, ok := modelButton(mainPicker, p, m, len(setup.Providers) > 1, false); ok {
			rows = append(rows, []notify.Button{b})
		}
	}
	rows = append(rows, []notify.Button{closeButton})
	text := fmt.Sprintf("Models with «%s»:", html.EscapeString(query))
	if len(found) > foundModels {
		text += fmt.Sprintf("\n\n%d more: write more of the name.", len(found)-foundModels)
	}
	c.sendHTML(ctx, telegram, text, rows)
}

// modelButton chooses model from provider. Telegram takes at most 64 bytes
// of data, so a longer name gets no button.
func modelButton(p picker, provider, model string, named, current bool) (notify.Button, bool) {
	data := p.choose + provider + "/" + model
	text := model
	if named {
		text += " · " + provider
	}
	return notify.Button{Text: mark(text, current), Data: data}, len(data) <= 64
}

// chooseModel makes the chat use model from provider from now on, or fx's
// own choice when both are empty, and returns the line that names the
// choice in Telegram's HTML format.
func (c *Chat) chooseModel(ctx context.Context, provider, model string) (string, error) {
	if model != "" {
		models, err := runner.Models(ctx, c.cfg, provider)
		if err != nil {
			return "", fmt.Errorf("Could not read the models: %v", err)
		}
		if !slices.Contains(models, model) {
			return "", fmt.Errorf("There is no model %s in %s. Send /model to see the list.", model, provider)
		}
	}
	c.choose(provider, model)

	name := c.modelName(ctx)
	if name == "" {
		name = "<b>" + html.EscapeString(model) + "</b>"
	}
	return "⚙︎ " + name, nil
}

func (c *Chat) choose(provider, model string) {
	levels := c.efforts(provider, model)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.state.Provider, c.state.Model = provider, model
	// A level the new model lacks would make fx refuse the turn.
	if !slices.Contains(levels, c.state.Effort) {
		c.state.Effort = ""
	}
	if model != "" {
		ref := provider + "/" + model
		recent := []string{ref}
		for _, r := range c.state.Recent {
			if r != ref && len(recent) < recentModels {
				recent = append(recent, r)
			}
		}
		c.state.Recent = recent
	}
	c.save()
}

func (c *Chat) show(ctx context.Context, telegram *notify.Telegram, edit int64, text string, rows [][]notify.Button) {
	if edit != 0 {
		c.editHTML(ctx, telegram, edit, text, rows)
		return
	}
	c.sendHTML(ctx, telegram, text, rows)
}

func (c *Chat) reply(ctx context.Context, telegram *notify.Telegram, text string, err error) {
	if err != nil {
		c.send(ctx, telegram, err.Error())
		return
	}
	c.sendHTML(ctx, telegram, text, nil)
}

// listEfforts offers the current model's reasoning effort levels.
func (c *Chat) listEfforts(ctx context.Context, telegram *notify.Telegram) {
	setup, provider, model, err := c.current(ctx)
	if err != nil {
		c.send(ctx, telegram, "Could not read the model: "+err.Error())
		return
	}
	levels := c.efforts(provider, model)
	if len(levels) == 0 {
		// den learns Codex's levels when fx lists its models.
		runner.Models(ctx, c.cfg, provider)
		levels = c.efforts(provider, model)
	}
	if len(levels) == 0 {
		c.sendHTML(ctx, telegram, "⚙︎ "+boldModel(model, setup)+" has no effort levels.", nil)
		return
	}

	c.mu.Lock()
	effort := c.state.Effort
	c.mu.Unlock()

	rows := effortRows("e:", levels, effort)
	c.sendHTML(ctx, telegram, "⚙︎ "+boldModel(model, setup)+"\n\nHow hard it thinks: more is slower and costs more.", append(rows, []notify.Button{closeButton}))
}

// effortsPerRow keeps the level names whole on a phone, where Telegram
// shortens the buttons of a crowded row.
const effortsPerRow = 4

// effortRows offers default and each level, marking the current one, with
// data that starts with prefix.
func effortRows(prefix string, levels []string, effort string) [][]notify.Button {
	buttons := []notify.Button{{Text: mark("default", effort == ""), Data: prefix + "default"}}
	for _, l := range levels {
		buttons = append(buttons, notify.Button{Text: mark(l, l == effort), Data: prefix + l})
	}

	var rows [][]notify.Button
	for len(buttons) > 0 {
		n := min(effortsPerRow, len(buttons))
		rows = append(rows, buttons[:n])
		buttons = buttons[n:]
	}
	return rows
}

// chooseEffort makes the chat use level from now on, or the model's own
// default for "default", and returns the line that names the choice.
func (c *Chat) chooseEffort(ctx context.Context, level string) (string, error) {
	setup, provider, model, err := c.current(ctx)
	if err != nil {
		return "", fmt.Errorf("Could not read the model: %v", err)
	}
	levels := c.efforts(provider, model)
	if level == "default" {
		level = ""
	} else if !slices.Contains(levels, level) {
		if len(levels) == 0 {
			return "", fmt.Errorf("%s has no effort levels.", model)
		}
		return "", fmt.Errorf("%s takes the effort %s, or default.", model, strings.Join(levels, ", "))
	}

	c.mu.Lock()
	c.state.Effort = level
	c.save()
	c.mu.Unlock()

	return "⚙︎ " + boldModel(model, setup) + effortName(level), nil
}

// modelName names the model the next turn uses in bold, in Telegram's HTML
// format, or returns "" when fx cannot tell.
func (c *Chat) modelName(ctx context.Context) string {
	c.mu.Lock()
	provider, model, effort := c.state.Provider, c.state.Model, c.state.Effort
	c.mu.Unlock()

	setup, err := runner.Status(ctx, c.cfg, provider)
	if err != nil {
		log.Printf("chat: %v", err)
		return ""
	}
	if model == "" {
		model = setup.Model
	}
	return boldModel(model, setup) + effortName(effort)
}

// boldModel names model in bold, and its provider too when the agent has
// signed in to more than one.
func boldModel(model string, setup runner.Setup) string {
	text := "<b>" + html.EscapeString(model) + "</b>"
	if len(setup.Providers) > 1 {
		text += " · " + html.EscapeString(setup.Provider)
	}
	return text
}

// effortName names a chosen effort after the model, and nothing for the
// model's default.
func effortName(effort string) string {
	if effort == "" {
		return ""
	}
	return " · effort " + html.EscapeString(effort)
}

// mark puts a dot before the current choice.
func mark(text string, current bool) string {
	if current {
		return "• " + text
	}
	return text
}
