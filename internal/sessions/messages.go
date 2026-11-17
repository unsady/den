package sessions

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Message is one message of a session as a chat shows it.
type Message struct {
	// Mine marks the user's message; the others are the agent's.
	Mine bool      `json:"mine"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// Messages returns the conversation of session id: the user's messages and,
// after each, the agent's last reply to it, as the chat sent it. den's own
// reports to the agent are left out, and so are subagents' reports with
// what the agent said to them, which the chat did not send.
func Messages(sessionsDir, id string) ([]Message, error) {
	f, err := os.Open(filepath.Join(sessionsDir, id, "log.jsonl"))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var list []Message
	// reply is the latest answer of the agent's to the turn, which a later
	// one in the same turn replaces.
	var reply *Message
	reporting := false
	flush := func() {
		if reply != nil {
			list = append(list, *reply)
			reply = nil
		}
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 64<<20)
	for scanner.Scan() {
		var e event
		if json.Unmarshal(scanner.Bytes(), &e) != nil || e.Kind != "item" {
			continue
		}
		text := textOf(e.Data)
		if text == "" {
			continue
		}

		switch e.Type {
		case "user":
			flush()
			reporting = strings.HasPrefix(text, reportPrefix)
			if reporting || strings.HasPrefix(text, "[den:") {
				continue
			}
			list = append(list, Message{Mine: true, Text: text, At: time.UnixMilli(e.TS)})
		case "assistant":
			if reporting {
				continue
			}
			reply = &Message{Text: text, At: time.UnixMilli(e.TS)}
		}
	}
	flush()
	return list, scanner.Err()
}
