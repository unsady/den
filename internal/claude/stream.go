package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// prefix starts the name of every tool den offers, as the model sees it.
const prefix = "mcp__den__"

// event is a line of claude's stdout, cut down to what the bridge reads.
type event struct {
	Type  string `json:"type"`
	Event struct {
		Type         string `json:"type"`
		Index        int    `json:"index"`
		ContentBlock struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"content_block"`
		Delta struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			PartialJSON string `json:"partial_json"`
			StopReason  string `json:"stop_reason"`
		} `json:"delta"`
		Message struct {
			Usage usage `json:"usage"`
		} `json:"message"`
		Usage usage `json:"usage"`
	} `json:"event"`
	IsError   bool   `json:"is_error"`
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
	// Models are the models of the turn, with their context windows.
	Models map[string]struct {
		Window int `json:"contextWindow"`
		Output int `json:"outputTokens"`
	} `json:"modelUsage"`
}

// window is the context window of the model that wrote the most.
func (e event) window() int {
	var size, most int
	for _, m := range e.Models {
		if m.Output >= most {
			size, most = m.Window, m.Output
		}
	}
	return size
}

type usage struct {
	Input         int `json:"input_tokens"`
	CacheRead     int `json:"cache_read_input_tokens"`
	CacheCreation int `json:"cache_creation_input_tokens"`
	Output        int `json:"output_tokens"`
}

func (u usage) prompt() int { return u.Input + u.CacheRead + u.CacheCreation }

// relay turns claude's events into one assistant message for fx. It ends
// with the message that asks for tools, or with the turn's result.
func (c *Claude) relay(ctx context.Context, s *session, rep *reply, record func(int)) {
	var (
		kinds = map[int]string{}
		calls = map[int]int{}
		stop  string
		used  usage
		ended bool
	)
	for {
		var ev event
		var ok bool
		select {
		case ev, ok = <-s.events:
		case <-ctx.Done():
			// fx went away: the turn is over, whatever claude was doing.
			s.stop()
			c.drop(s)
			return
		}
		if !ok {
			if s.resumed {
				log.Printf("claude: resumed process stopped: %s", s.exit)
			}
			rep.fail("claude stopped: " + s.exit)
			return
		}

		switch ev.Type {
		case "stream_event":
			e := ev.Event
			switch e.Type {
			case "message_start":
				kinds, calls, stop, ended = map[int]string{}, map[int]int{}, "", false
				used = e.Message.Usage
			case "content_block_start":
				kinds[e.Index] = e.ContentBlock.Type
				if e.ContentBlock.Type == "tool_use" {
					calls[e.Index] = rep.addCall(e.ContentBlock.ID, strings.TrimPrefix(e.ContentBlock.Name, prefix))
				}
			case "content_block_delta":
				switch {
				case e.Delta.Type == "text_delta" && kinds[e.Index] == "text":
					rep.addText(e.Delta.Text)
				case e.Delta.Type == "input_json_delta" && kinds[e.Index] == "tool_use":
					rep.addArgs(calls[e.Index], e.Delta.PartialJSON)
				}
			case "content_block_stop":
				// A tool without arguments streams none, and fx wants JSON.
				if i, ok := calls[e.Index]; ok && rep.calls[i].args.Len() == 0 {
					rep.addArgs(i, "{}")
				}
			case "message_delta":
				stop = e.Delta.StopReason
				used = e.Usage
			case "message_stop":
				ended = true
				if stop == "tool_use" {
					s.emit(rep)
					rep.finish("tool_calls", used)
					record(used.prompt())
					return
				}
			}
		case "result":
			if ev.IsError {
				if s.resumed {
					log.Printf("claude: resumed process failed: %s", clip(ev.Result))
				}
				rep.fail(ev.Result)
				s.stop()
				c.drop(s)
				return
			}
			if !ended {
				continue
			}
			// After the reply: fx should not wait on den's files.
			defer c.learn(rep.model, ev.window())
			reason := "stop"
			if stop == "max_tokens" {
				reason = "length"
			}
			if len(s.tools) == 0 {
				// A request without tools, such as fx naming a session,
				// is not a conversation: nothing will continue it, and
				// its process would only take a place.
				c.drop(s)
				s.shut()
				rep.finish(reason, used)
				record(used.prompt())
				return
			}
			// The process waits for the next turn.
			s.finish(rep)
			c.rest(s)
			rep.finish(reason, used)
			record(used.prompt())
			return
		}
	}
}

// reply is the assistant message fx gets, sent as it grows when fx asked
// for a stream and in one piece when it did not.
type reply struct {
	w      http.ResponseWriter
	stream bool
	id     string
	model  string
	sent   bool
	text   strings.Builder
	calls  []*call
}

type call struct {
	id, name string
	args     strings.Builder
}

func (r *reply) addText(s string) {
	r.text.WriteString(s)
	r.chunk(map[string]any{"content": s}, nil)
}

// addCall starts a tool call and returns its index.
func (r *reply) addCall(id, name string) int {
	r.calls = append(r.calls, &call{id: id, name: name})
	i := len(r.calls) - 1
	r.chunk(map[string]any{"tool_calls": []any{map[string]any{
		"index": i, "id": id, "type": "function",
		"function": map[string]any{"name": name, "arguments": ""},
	}}}, nil)
	return i
}

func (r *reply) addArgs(i int, s string) {
	r.calls[i].args.WriteString(s)
	r.chunk(map[string]any{"tool_calls": []any{map[string]any{
		"index": i, "function": map[string]any{"arguments": s},
	}}}, nil)
}

// chunk streams one piece of the message.
func (r *reply) chunk(delta map[string]any, finish any) {
	if !r.stream {
		return
	}
	if !r.sent {
		r.w.Header().Set("Content-Type", "text/event-stream")
		r.w.Header().Set("Cache-Control", "no-cache")
		r.sent = true
		delta["role"] = "assistant"
	}
	r.write(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
}

func (r *reply) write(body map[string]any) {
	body["id"], body["object"], body["created"], body["model"] = r.id, "chat.completion.chunk", time.Now().Unix(), r.model
	data, _ := json.Marshal(body)
	fmt.Fprintf(r.w, "data: %s\n\n", data)
	if f, ok := r.w.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *reply) finish(reason string, u usage) {
	usage := map[string]int{"prompt_tokens": u.prompt(), "completion_tokens": u.Output, "total_tokens": u.prompt() + u.Output}
	if r.stream {
		r.chunk(map[string]any{}, reason)
		r.write(map[string]any{"choices": []any{}, "usage": usage})
		fmt.Fprint(r.w, "data: [DONE]\n\n")
		if f, ok := r.w.(http.Flusher); ok {
			f.Flush()
		}
		return
	}

	msg := map[string]any{"role": "assistant", "content": nil}
	if r.text.Len() > 0 {
		msg["content"] = r.text.String()
	}
	if len(r.calls) > 0 {
		var calls []any
		for _, c := range r.calls {
			calls = append(calls, map[string]any{"id": c.id, "type": "function", "function": map[string]any{"name": c.name, "arguments": c.args.String()}})
		}
		msg["tool_calls"] = calls
	}
	w := r.w
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"id": r.id, "object": "chat.completion", "created": time.Now().Unix(), "model": r.model,
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": reason}},
		"usage":   usage,
	})
}

// fail ends the reply with an error: an HTTP one if fx has seen nothing
// yet, and otherwise a last chunk that says why the stream stops.
func (r *reply) fail(msg string) {
	if !r.sent {
		http.Error(r.w, msg, http.StatusBadGateway)
		return
	}
	data, _ := json.Marshal(map[string]any{"error": map[string]string{"message": msg}})
	fmt.Fprintf(r.w, "data: %s\n\n", data)
}
