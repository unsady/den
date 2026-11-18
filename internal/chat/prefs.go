package chat

// What the chat keeps in den's store besides its sessions: the two models,
// the models chosen last, where it is in Telegram's updates, and the
// messages that wait for a turn.

import (
	"context"
	"log"

	"github.com/unsady/den/internal/runner"
	"github.com/unsady/den/internal/store"
)

// mainModel is what /model and /effort chose, empty for fx's own choice.
func (c *Chat) mainModel() runner.Choice {
	m, _, err := c.store.Model(context.Background(), store.Main)
	if err != nil {
		log.Printf("chat: model: %v", err)
	}
	return runner.Choice(m)
}

func (c *Chat) setMainModel(choice runner.Choice) {
	if err := c.store.SetModel(context.Background(), store.Main, store.Choice(choice)); err != nil {
		log.Printf("chat: model: %v", err)
	}
}

// second is the second model, nil while none is chosen.
func (c *Chat) second() *runner.Choice {
	m, ok, err := c.store.Model(context.Background(), store.Second)
	if err != nil {
		log.Printf("chat: second model: %v", err)
	}
	if !ok {
		return nil
	}
	choice := runner.Choice(m)
	return &choice
}

// setSecond chooses the second model, or removes it when choice is nil.
func (c *Chat) setSecond(choice *runner.Choice) {
	ctx := context.Background()
	var err error
	if choice == nil {
		err = c.store.ClearModel(ctx, store.Second)
	} else {
		err = c.store.SetModel(ctx, store.Second, store.Choice(*choice))
	}
	if err != nil {
		log.Printf("chat: second model: %v", err)
	}
}

// recent are the models chosen last, as provider/model, newest first.
func (c *Chat) recent() []string {
	list, err := c.store.RecentModels(context.Background())
	if err != nil {
		log.Printf("chat: recent models: %v", err)
	}
	refs := make([]string, 0, len(list))
	for _, m := range list {
		refs = append(refs, m.Provider+"/"+m.Model)
	}
	return refs
}

func (c *Chat) offset() int64 {
	n, err := c.store.TelegramOffset(context.Background())
	if err != nil {
		log.Printf("chat: offset: %v", err)
	}
	return n
}

// setOffset records that the updates before next were read, before their
// turns run, so a turn that crashes den is not read again on restart.
func (c *Chat) setOffset(next int64) {
	if err := c.store.SetTelegramOffset(context.Background(), next); err != nil {
		log.Printf("chat: offset: %v", err)
	}
}

// enqueue keeps entries for the next turns of their sessions and wakes the
// worker.
func (c *Chat) enqueue(entries ...entry) error {
	messages := make([]store.Message, 0, len(entries))
	for _, e := range entries {
		messages = append(messages, store.Message{
			Session: e.session, Text: e.text, Image: e.image, App: e.app, Report: e.report, Notice: e.notice,
		})
	}
	if err := c.store.Enqueue(context.Background(), messages...); err != nil {
		return err
	}
	c.poke()
	return nil
}

// waiting says whether messages wait for a turn.
func (c *Chat) waiting() bool {
	n, err := c.store.Waiting(context.Background())
	if err != nil {
		log.Printf("chat: inbox: %v", err)
	}
	return n > 0
}
