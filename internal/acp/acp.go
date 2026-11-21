// Package acp is a client for the Agent Client Protocol that fx speaks with
// `fx acp`: JSON-RPC 2.0, one JSON message per line, over the process's
// standard input and output.
//
// A Conn serves one fx process. fx keeps one active session per process: a
// session/new or session/resume makes that session the active one and
// cancels the turn of the session before it, so den runs one process per
// session. The Conn itself keeps sessions apart by id and does not depend on
// that.
package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// protocolVersion is the ACP version den speaks.
const protocolVersion = 1

// cancelWait is how long Prompt waits for the answer to a cancelled turn.
var cancelWait = 10 * time.Second

// closeWait is how long Close waits for the process to exit after its input
// closed, before it kills the process.
var closeWait = 5 * time.Second

// ErrClosed is the error of a call on a Conn whose server has gone.
var ErrClosed = errors.New("acp: the server has exited")

// ErrIdle is the error of a Steer in a session where nothing runs. Send a
// Prompt instead.
var ErrIdle = errors.New("acp: no turn runs in the session to steer")

// ErrBusy is the error of a Prompt in a session where a steer that fx runs
// as a turn of its own is still streaming. See Conn.OnTurn.
var ErrBusy = errors.New("acp: a steered turn still runs in the session")

// Error is a JSON-RPC error that the server answered a request with.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("acp: server error %d: %s", e.Code, e.Message)
}

// codeNotFound is ACP's code for a resource that does not exist, which fx
// answers a session id with no saved session with.
const codeNotFound = -32002

// NotFound says whether err is fx's answer that a session does not exist.
func NotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == codeNotFound
}

// The kinds of Event.
const (
	KindText       = "text"
	KindThought    = "thought"
	KindToolCall   = "tool_call"
	KindToolUpdate = "tool_update"
	KindUsage      = "usage"
	KindNotice     = "notice"
	KindOther      = "other"
)

// Event is one session/update of a running prompt.
type Event struct {
	Kind string
	// Text is the chunk for text and thought, the title of a notice, and the
	// output of a tool_update when it has any.
	Text string
	// ToolCallID, Title, ToolKind, and Status describe a tool call. Status
	// is also the severity of a notice.
	ToolCallID string
	Title      string
	ToolKind   string
	Status     string
	// InputTokens and OutputTokens are 0 in a usage event: fx reports the
	// tokens of a turn in the answer to the prompt, see Result.
	InputTokens  int
	OutputTokens int
	// Used and Size are the tokens in the context and the size of the
	// context window, in a usage event.
	Used int
	Size int
	// Raw is the update as the server sent it.
	Raw json.RawMessage
}

// Prompt is the user's turn.
type Prompt struct {
	Text   string
	Images []Image
}

// Image is a picture of a prompt.
type Image struct {
	// MimeType is such as "image/png".
	MimeType string
	Data     []byte
}

// Result is how a prompt ended.
type Result struct {
	// StopReason is such as "end_turn", "cancelled", or "refused".
	StopReason string
	// Text is the agent_message_chunk text of the turn, joined.
	Text         string
	InputTokens  int
	OutputTokens int
}

// message is any JSON-RPC message.
type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`

	// gone is set by the Conn on the answer it makes up for a request that
	// the server never answered.
	gone error
}

// Conn is one running fx acp server.
type Conn struct {
	// OnDropped is called when fx did not deliver a steer, with the session
	// and the key given to Steer. Any error fx answers a steer with counts,
	// and so does the server exiting before it answers. Set it before the
	// first Steer. It runs on its own goroutine.
	OnDropped func(session, key string)

	// OnTurn is called when fx ran a steer as a turn of its own, which it
	// does when the running turn had already closed its input. res is that
	// turn's result, built as Prompt builds it. Set it before the first
	// Steer. It runs on its own goroutine. Until that turn answers, Prompt
	// in the session returns ErrBusy.
	OnTurn func(session, key string, res Result)

	w   io.Writer
	wmu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]func(*message)
	steers  map[int64]*steer
	prompts map[string]*turn
	err     error
	// answered closes when fx answers a steer, and a new one takes its
	// place.
	answered chan struct{}

	done chan struct{}

	cmd   *exec.Cmd
	stdin io.Closer
}

// steer is a steer that fx has not answered yet. When fx runs it as a turn
// of its own, the turn's text collects here.
type steer struct {
	session string
	key     string
	// running is set when updates arrived for the steer, which then is a
	// turn of its own.
	running bool
	reply   strings.Builder
}

// Running reports whether a Prompt or a steered turn runs in the session.
func (c *Conn) Running(session string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runningLocked(session)
}

func (c *Conn) runningLocked(session string) bool {
	if c.prompts[session] != nil {
		return true
	}
	for _, s := range c.steers {
		if s.session == session && s.running {
			return true
		}
	}
	return false
}

// New runs a Conn over the server's output r and input w.
func New(r io.Reader, w io.Writer) *Conn {
	c := &Conn{
		w:        w,
		pending:  map[int64]func(*message){},
		steers:   map[int64]*steer{},
		prompts:  map[string]*turn{},
		answered: make(chan struct{}),
		done:     make(chan struct{}),
	}
	go c.read(r)
	return c
}

// Start starts cmd, which must run `fx acp`, and initializes the connection.
// It takes cmd's standard input and output.
func Start(ctx context.Context, cmd *exec.Cmd) (*Conn, error) {
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}

	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	err = cmd.Start()
	if err != nil {
		return nil, err
	}

	c := New(out, in)
	c.cmd = cmd
	c.stdin = in

	err = c.Initialize(ctx)
	if err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// Done is closed when the server's output ends, because it exited or
// crashed.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Close closes the server's input and waits for it to exit. A server that
// started with Start and does not exit is killed after a few seconds.
func (c *Conn) Close() error {
	if c.stdin != nil {
		c.stdin.Close()
	}
	if c.cmd == nil {
		return nil
	}

	exited := make(chan error, 1)
	go func() { exited <- c.cmd.Wait() }()

	select {
	case err := <-exited:
		return err
	case <-time.After(closeWait):
		c.cmd.Process.Kill()
		<-exited
		return nil
	}
}

// Initialize negotiates the protocol. den offers no file system or terminal
// capabilities, and asks for notices as session/update notices: without that
// fx puts them into the agent's reply text.
func (c *Conn) Initialize(ctx context.Context) error {
	_, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"clientCapabilities": map[string]any{
			"session": map[string]any{"notices": map[string]any{}},
		},
	})
	return err
}

// NewSession opens a session that works in cwd and returns its id: id,
// when it is not empty, or one fx chooses. fx saves the session with its
// first prompt, and until then a resume of it finds nothing.
func (c *Conn) NewSession(ctx context.Context, cwd, id string) (string, error) {
	params := map[string]any{
		"cwd":        cwd,
		"mcpServers": []any{},
	}
	if id != "" {
		params["_meta"] = map[string]any{"fx": map[string]any{"sessionId": id}}
	}
	res, err := c.call(ctx, "session/new", params)
	if err != nil {
		return "", err
	}

	var out struct {
		SessionID string `json:"sessionId"`
	}
	err = json.Unmarshal(res, &out)
	if err != nil {
		return "", fmt.Errorf("acp: bad session/new result: %w", err)
	}
	if out.SessionID == "" {
		return "", errors.New("acp: session/new returned no session id")
	}
	return out.SessionID, nil
}

// Resume continues the saved session id in cwd. It uses session/resume,
// which restores the session without replaying its history as updates,
// where session/load sends every earlier message again.
func (c *Conn) Resume(ctx context.Context, id, cwd string) error {
	_, err := c.call(ctx, "session/resume", map[string]any{
		"sessionId":  id,
		"cwd":        cwd,
		"mcpServers": []any{},
	})
	return err
}

// Compaction is what came of compacting a session: whether fx compacted it,
// and its size before and after in fx's estimate of tokens.
type Compaction struct {
	Compacted bool `json:"compacted"`
	Before    int  `json:"tokensBefore"`
	After     int  `json:"tokensAfter"`
}

// Compact compacts session in place, under its id, with the fx fork's
// libfx/compact. The session must be open and idle.
func (c *Conn) Compact(ctx context.Context, session string) (Compaction, error) {
	var out Compaction
	raw, err := c.call(ctx, "libfx/compact", map[string]any{"sessionId": session})
	if err != nil {
		return out, err
	}
	return out, json.Unmarshal(raw, &out)
}

// SetModel sets the model of the session and then its reasoning effort,
// with session/set_config_option for the options "model" and "effort". An
// empty model or effort is left as it is. The model must be one that fx's
// provider offers, and the effort one that the model supports; "auto" is the
// default effort. Set the model first, because the efforts depend on it.
func (c *Conn) SetModel(ctx context.Context, session, model, effort string) error {
	for _, option := range [...][2]string{{"model", model}, {"effort", effort}} {
		if option[1] == "" {
			continue
		}

		_, err := c.call(ctx, "session/set_config_option", map[string]any{
			"sessionId": session,
			"configId":  option[0],
			"value":     option[1],
		})
		if err != nil {
			return fmt.Errorf("set %s: %w", option[0], err)
		}
	}
	return nil
}

// Cancel asks the server to stop the running turn of the session. The
// Prompt of that turn then ends with the stop reason "cancelled".
func (c *Conn) Cancel(session string) error {
	return c.notify("session/cancel", map[string]any{"sessionId": session})
}

// Steer adds text to the turn that runs in the session, which fx takes in at
// its next safe point. It returns when the request is written. fx answers
// when the turn ends: a steer that the turn did not take is reported to
// OnDropped. A steer that arrives just as the turn ends runs as a turn of
// its own, which is reported to OnTurn. Both get key, which tells the
// caller's steers apart. With nothing running in the session Steer returns
// ErrIdle and sends nothing.
func (c *Conn) Steer(ctx context.Context, session, key, text string) error {
	params := map[string]any{
		"sessionId": session,
		"prompt":    []any{map[string]any{"type": "text", "text": text}},
		"_meta":     map[string]any{"fx": map[string]any{"steer": true}},
	}

	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return c.closedErr()
	}
	if !c.runningLocked(session) {
		c.mu.Unlock()
		return ErrIdle
	}
	c.nextID++
	id := c.nextID
	c.steers[id] = &steer{session: session, key: key}
	c.pending[id] = c.steerAnswered(id)
	c.mu.Unlock()

	err := c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": "session/prompt", "params": params})
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.forgetLocked(id)
		c.mu.Unlock()
	}
	return err
}

// forgetLocked drops the steer id and wakes Settle. The caller holds c.mu.
func (c *Conn) forgetLocked(id int64) {
	delete(c.steers, id)
	close(c.answered)
	c.answered = make(chan struct{})
}

// Settle returns once fx has answered every steer, which for a steer that
// fx runs as a turn of its own is when that turn ends, or when ctx ends or
// the connection closes.
func (c *Conn) Settle(ctx context.Context) {
	for {
		c.mu.Lock()
		left := len(c.steers)
		answered := c.answered
		c.mu.Unlock()
		if left == 0 {
			return
		}

		select {
		case <-answered:
		case <-c.done:
			return
		case <-ctx.Done():
			return
		}
	}
}

// steerAnswered handles the answer to the steer id. The caller holds c.mu
// when it makes the handler, and the reader calls the handler without it.
func (c *Conn) steerAnswered(id int64) func(*message) {
	return func(m *message) {
		c.mu.Lock()
		st := c.steers[id]
		c.forgetLocked(id)
		c.mu.Unlock()

		if st == nil {
			return
		}

		// fx answers a steer the turn ended before with an error, and its
		// message is no contract, so any error means it was not delivered.
		dropped := m.gone != nil || m.Error != nil
		if !dropped {
			var meta struct {
				Meta struct {
					Fx struct {
						Steering string `json:"steering"`
					} `json:"fx"`
				} `json:"_meta"`
			}
			json.Unmarshal(m.Result, &meta)

			switch meta.Meta.Fx.Steering {
			case "absorbed":
				return
			case "":
				res, err := parseResult(m.Result)
				if err == nil && c.OnTurn != nil {
					res.Text = st.reply.String()
					go c.OnTurn(st.session, st.key, res)
				}
				return
			}
			dropped = true
		}
		if dropped && c.OnDropped != nil {
			go c.OnDropped(st.session, st.key)
		}
	}
}

// parseResult reads the result of a session/prompt, except for its text.
func parseResult(raw json.RawMessage) (Result, error) {
	var out struct {
		StopReason string `json:"stopReason"`
		Usage      struct {
			InputTokens  int `json:"inputTokens"`
			OutputTokens int `json:"outputTokens"`
		} `json:"usage"`
	}
	err := json.Unmarshal(raw, &out)
	if err != nil {
		return Result{}, fmt.Errorf("acp: bad session/prompt result: %w", err)
	}
	return Result{
		StopReason:   out.StopReason,
		InputTokens:  out.Usage.InputTokens,
		OutputTokens: out.Usage.OutputTokens,
	}, nil
}

// turn is a running Prompt: the updates and the answer, queued in the order
// they arrived.
type turn struct {
	mu    sync.Mutex
	queue []queued
	wake  chan struct{}
}

type queued struct {
	update json.RawMessage
	answer *message
}

func (t *turn) push(q queued) {
	t.mu.Lock()
	t.queue = append(t.queue, q)
	t.mu.Unlock()

	select {
	case t.wake <- struct{}{}:
	default:
	}
}

func (t *turn) take() []queued {
	t.mu.Lock()
	defer t.mu.Unlock()
	q := t.queue
	t.queue = nil
	return q
}

// Prompt sends the user's turn and returns when the agent has answered.
// events gets every update of the session meanwhile, in order, on the
// goroutine of this call; it may be nil. When ctx ends, Prompt cancels the
// turn, waits a few seconds for the answer, and returns ctx.Err() with what
// the turn had by then.
func (c *Conn) Prompt(ctx context.Context, session string, p Prompt, events func(Event)) (Result, error) {
	blocks := []any{}
	if p.Text != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": p.Text})
	}
	for _, img := range p.Images {
		blocks = append(blocks, map[string]any{"type": "image", "mimeType": img.MimeType, "data": img.Data})
	}

	t := &turn{wake: make(chan struct{}, 1)}

	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return Result{}, c.closedErr()
	}
	if c.prompts[session] != nil {
		c.mu.Unlock()
		return Result{}, fmt.Errorf("acp: session %s already has a prompt running", session)
	}
	if c.runningLocked(session) {
		c.mu.Unlock()
		return Result{}, fmt.Errorf("session %s: %w", session, ErrBusy)
	}
	c.prompts[session] = t
	c.nextID++
	id := c.nextID
	// The turn ends for the reader at its answer, so that updates after it
	// belong to the next turn, such as a steer that fx runs on its own.
	c.pending[id] = func(m *message) {
		c.mu.Lock()
		if c.prompts[session] == t {
			delete(c.prompts, session)
		}
		c.mu.Unlock()

		t.push(queued{answer: m})
	}
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		if c.prompts[session] == t {
			delete(c.prompts, session)
		}
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	err := c.send(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "session/prompt",
		"params":  map[string]any{"sessionId": session, "prompt": blocks},
	})
	if err != nil {
		return Result{}, err
	}

	var (
		res       Result
		text      strings.Builder
		ctxDone   = ctx.Done()
		cancelled error
		deadline  <-chan time.Time
	)
	for {
		for _, q := range t.take() {
			if q.answer == nil {
				ev := parseEvent(q.update)
				if ev.Kind == KindText {
					text.WriteString(ev.Text)
				}
				if events != nil {
					events(ev)
				}
				continue
			}

			res.Text = text.String()
			m := q.answer
			if m.gone != nil {
				return res, m.gone
			}
			if m.Error != nil {
				return res, m.Error
			}

			parsed, err := parseResult(m.Result)
			if err != nil {
				return res, err
			}
			parsed.Text = res.Text
			return parsed, cancelled
		}

		select {
		case <-t.wake:
		case <-ctxDone:
			cancelled = ctx.Err()
			ctxDone = nil
			deadline = time.After(cancelWait)
			c.Cancel(session)
		case <-deadline:
			res.Text = text.String()
			return res, cancelled
		}
	}
}

// parseEvent reads a session/update's update object.
func parseEvent(raw json.RawMessage) Event {
	var u struct {
		Kind       string          `json:"sessionUpdate"`
		ToolCallID string          `json:"toolCallId"`
		Title      string          `json:"title"`
		ToolKind   string          `json:"kind"`
		Status     string          `json:"status"`
		Severity   string          `json:"severity"`
		Used       int             `json:"used"`
		Size       int             `json:"size"`
		Content    json.RawMessage `json:"content"`
	}
	json.Unmarshal(raw, &u)

	ev := Event{Kind: KindOther, Raw: raw}
	switch u.Kind {
	case "agent_message_chunk":
		ev.Kind = KindText
		ev.Text = chunkText(u.Content)
	case "agent_thought_chunk":
		ev.Kind = KindThought
		ev.Text = chunkText(u.Content)
	case "tool_call":
		ev.Kind = KindToolCall
		ev.ToolCallID, ev.Title, ev.ToolKind, ev.Status = u.ToolCallID, u.Title, u.ToolKind, u.Status
	case "tool_call_update":
		ev.Kind = KindToolUpdate
		ev.ToolCallID, ev.Title, ev.ToolKind, ev.Status = u.ToolCallID, u.Title, u.ToolKind, u.Status
		ev.Text = toolOutput(u.Content)
	case "usage_update":
		ev.Kind = KindUsage
		ev.Used, ev.Size = u.Used, u.Size
	case "notice":
		ev.Kind = KindNotice
		ev.Text, ev.Status = u.Title, u.Severity
	}
	return ev
}

// chunkText is the text of a content block, such as {"type":"text",...}.
func chunkText(raw json.RawMessage) string {
	var b struct {
		Text string `json:"text"`
	}
	json.Unmarshal(raw, &b)
	return b.Text
}

// toolOutput is the text of a tool_call_update's content list.
func toolOutput(raw json.RawMessage) string {
	var items []struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return ""
	}

	var parts []string
	for _, item := range items {
		if t := chunkText(item.Content); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n")
}

// call sends a request and waits for its answer.
func (c *Conn) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	answer := make(chan *message, 1)

	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return nil, c.closedErr()
	}
	c.nextID++
	id := c.nextID
	c.pending[id] = func(m *message) { answer <- m }
	c.mu.Unlock()

	err := c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}

	select {
	case m := <-answer:
		if m.gone != nil {
			return nil, m.gone
		}
		if m.Error != nil {
			return nil, m.Error
		}
		return m.Result, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (c *Conn) notify(method string, params any) error {
	return c.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// send writes one message as a line.
func (c *Conn) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')

	c.wmu.Lock()
	defer c.wmu.Unlock()

	_, err = c.w.Write(b)
	if err != nil {
		return fmt.Errorf("acp: write: %w", err)
	}
	return nil
}

// closedErr is the error of a call made after the server's output ended.
// The caller holds c.mu or knows that c.err is set.
func (c *Conn) closedErr() error {
	if c.err == nil || c.err == io.EOF {
		return ErrClosed
	}
	return fmt.Errorf("%w: %v", ErrClosed, c.err)
}

// read dispatches what the server sends until its output ends.
func (c *Conn) read(r io.Reader) {
	sc := bufio.NewReaderSize(r, 64*1024)
	var err error
	for {
		var line []byte
		line, err = sc.ReadBytes('\n')
		if len(line) > 0 {
			c.dispatch(line)
		}
		if err != nil {
			break
		}
	}

	c.mu.Lock()
	c.err = err
	gone := c.closedErr()
	pending := c.pending
	c.pending = map[int64]func(*message){}
	c.mu.Unlock()

	close(c.done)
	for _, h := range pending {
		h(&message{gone: gone})
	}
}

func (c *Conn) dispatch(line []byte) {
	var m message
	if json.Unmarshal(line, &m) != nil {
		return
	}

	switch {
	case m.Method != "" && len(m.ID) > 0:
		go c.serve(&m)
	case m.Method == "session/update":
		c.update(m.Params)
	case m.Method == "" && len(m.ID) > 0:
		var id int64
		if json.Unmarshal(m.ID, &id) != nil {
			return
		}

		c.mu.Lock()
		h := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if h != nil {
			h(&m)
		}
	}
}

// update queues a session/update for the prompt that runs in its session.
func (c *Conn) update(params json.RawMessage) {
	var p struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}

	c.mu.Lock()
	t := c.prompts[p.SessionID]
	if t == nil {
		// No Prompt runs, so the update belongs to a steer that fx runs as
		// a turn: the oldest unanswered one of the session.
		var oldest int64
		for id, s := range c.steers {
			if s.session == p.SessionID && (oldest == 0 || id < oldest) {
				oldest = id
			}
		}
		if s := c.steers[oldest]; s != nil {
			s.running = true
			if ev := parseEvent(p.Update); ev.Kind == KindText {
				s.reply.WriteString(ev.Text)
			}
		}
	}
	c.mu.Unlock()
	if t != nil {
		t.push(queued{update: p.Update})
	}
}

// serve answers a request of the server, so that it never waits for us.
// den runs fx with full access and offers no capabilities, so the only
// request to expect is session/request_permission, which gets the option
// that allows once. Anything else, such as a permission request without
// that option, is a method that does not exist here.
func (c *Conn) serve(m *message) {
	reply := map[string]any{"jsonrpc": "2.0", "id": m.ID}

	if m.Method == "session/request_permission" {
		var p struct {
			Options []struct {
				ID   string `json:"optionId"`
				Kind string `json:"kind"`
			} `json:"options"`
		}
		json.Unmarshal(m.Params, &p)

		for _, o := range p.Options {
			if o.Kind == "allow_once" {
				reply["result"] = map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": o.ID}}
				break
			}
		}
	}
	if reply["result"] == nil {
		reply["error"] = map[string]any{"code": -32601, "message": "Method not found"}
	}

	c.send(reply)
}
