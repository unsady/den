package chat

import (
	"context"
	"log"
	"time"

	"github.com/unsady/den/internal/notify"
)

// setMenu sends the command menu and reports whether Telegram took it. A
// failure is retried on the next poll.
func (c *Chat) setMenu(ctx context.Context, telegram *notify.Telegram) bool {
	call, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := telegram.SetCommands(call, commands); err != nil {
		log.Printf("chat: commands: %v", err)
		return false
	}
	return true
}

func (c *Chat) send(ctx context.Context, telegram *notify.Telegram, text string) {
	call, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := telegram.Send(call, text); err != nil {
		log.Printf("chat: send: %v", err)
	}
}

func (c *Chat) editPlain(ctx context.Context, telegram *notify.Telegram, id int64, text string) {
	call, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := telegram.Edit(call, id, text); err != nil {
		log.Printf("chat: edit: %v", err)
	}
}

func (c *Chat) sendHTML(ctx context.Context, telegram *notify.Telegram, text string, rows [][]notify.Button) int64 {
	call, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	id, err := telegram.SendHTML(call, text, rows)
	if err != nil {
		log.Printf("chat: send: %v", err)
	}
	return id
}

func (c *Chat) editHTML(ctx context.Context, telegram *notify.Telegram, id int64, text string, rows [][]notify.Button) {
	call, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := telegram.EditHTML(call, id, text, rows); err != nil {
		log.Printf("chat: edit: %v", err)
	}
}

func (c *Chat) sendButtons(ctx context.Context, telegram *notify.Telegram, text string, rows [][]notify.Button) {
	call, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if _, err := telegram.SendButtons(call, text, rows); err != nil {
		log.Printf("chat: send: %v", err)
	}
}
