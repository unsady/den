package chat

import (
	"context"
	"fmt"
	"html"
	"log"
	"slices"
	"strings"

	"github.com/unsady/den/internal/jobs"
	"github.com/unsady/den/internal/notify"
	"github.com/unsady/den/internal/runner"
)

// The chat has two models: the main one, which the chat itself and most
// jobs run on, and the second, which the user sets for the jobs that need
// another model, such as a stronger or a faster one. Jobs name a model only
// by its slot, so changing the second model changes every job on it.

// Choice returns the model and effort a job on slot runs on. The second
// slot falls back to the main model while it is unset.
func (c *Chat) Choice(ctx context.Context, slot string) runner.Choice {
	choice := c.mainModel()
	if second := c.second(); slot == jobs.Second && second != nil {
		choice = *second
	}

	// fx's own choice is named, so the job's reply can say which model
	// wrote it.
	if choice.Model == "" {
		setup, err := runner.Status(ctx, c.cfg, choice.Provider)
		if err != nil {
			log.Printf("chat: %v", err)
			return choice
		}
		choice.Provider, choice.Model = setup.Provider, setup.Model
	}
	return choice
}

// secondLine names the second model, or says there is none.
func (c *Chat) secondLine(setup runner.Setup) string {
	second := c.second()
	if second == nil {
		return "Second: none"
	}
	return "Second: " + secondName(*second, setup)
}

func secondName(second runner.Choice, setup runner.Setup) string {
	text := "<b>" + html.EscapeString(second.Model) + "</b>"
	if len(setup.Providers) > 1 {
		text += " · " + html.EscapeString(second.Provider)
	}
	return text + effortName(second.Effort)
}

// chooseSecond makes model from provider the second model, or removes it
// when model is empty. It returns the line that names the choice and, when
// the model has them, buttons for its effort levels.
func (c *Chat) chooseSecond(ctx context.Context, provider, model string) (string, [][]notify.Button, error) {
	setup, err := runner.Status(ctx, c.cfg, "")
	if err != nil {
		return "", nil, fmt.Errorf("Could not read the models: %v", err)
	}

	if model == "" {
		c.setSecond(nil)
		return "⚙︎ Second: none. Jobs set to it run on the main model.", nil, nil
	}

	models, err := runner.Models(ctx, c.cfg, provider)
	if err != nil {
		return "", nil, fmt.Errorf("Could not read the models: %v", err)
	}
	if !slices.Contains(models, model) {
		return "", nil, fmt.Errorf("There is no model %s in %s.", model, provider)
	}

	choice := runner.Choice{Provider: provider, Model: model}
	// The effort carries over when the new model has the same level.
	if old := c.second(); old != nil && slices.Contains(c.efforts(provider, model), old.Effort) {
		choice.Effort = old.Effort
	}
	c.setSecond(&choice)

	return "⚙︎ Second: " + secondName(choice, setup), c.secondEfforts(provider, model, choice.Effort), nil
}

// secondEfforts offers the second model's effort levels, or nothing when it
// has none.
func (c *Chat) secondEfforts(provider, model, effort string) [][]notify.Button {
	levels := c.efforts(provider, model)
	if len(levels) == 0 {
		return nil
	}

	return effortRows("f:", levels, effort)
}

// chooseSecondEffort sets the second model's effort, or its own default
// for "default".
func (c *Chat) chooseSecondEffort(ctx context.Context, level string) (string, error) {
	setup, err := runner.Status(ctx, c.cfg, "")
	if err != nil {
		return "", fmt.Errorf("Could not read the models: %v", err)
	}

	second := c.second()
	if second == nil {
		return "", fmt.Errorf("There is no second model. Choose one in /model.")
	}
	levels := c.efforts(second.Provider, second.Model)
	if level == "default" {
		level = ""
	} else if !slices.Contains(levels, level) {
		return "", fmt.Errorf("%s takes the effort %s, or default.", second.Model, strings.Join(levels, ", "))
	}

	choice := *second
	choice.Effort = level
	c.setSecond(&choice)

	return "⚙︎ Second: " + secondName(choice, setup), nil
}
