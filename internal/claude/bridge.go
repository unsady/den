package claude

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// bridge holds the claude processes that answer fx, one per conversation
// in progress, and the secret that guards their MCP endpoint.
type bridge struct {
	mu       sync.Mutex
	secret   string
	sessions map[string]*session
	// closed are the sessions whose process ended but that claude can
	// resume, the oldest first.
	closed []*closedSession
	// idle is how long a session waits for its next turn before its
	// process is closed, and max how many processes may live at once.
	// Zero takes the default.
	idle time.Duration
	max  int
	// free reports the memory the container has left, and false when it
	// cannot tell. Zero reads the cgroup.
	free func() (int64, bool)
}

// minFree is the memory a process over the limit needs, with some to
// spare: a claude process takes about 230 MB.
const minFree = 300 << 20

// errNoMemory is why a process over the limit does not start.
var errNoMemory = errors.New("den has no memory for another Claude process; it retries")

// freeMemory is what the container's cgroup has left. Without a limit or a
// cgroup v2 it cannot tell.
func freeMemory() (int64, bool) {
	limit, ok := cgroupValue("/sys/fs/cgroup/memory.max")
	if !ok {
		return 0, false
	}
	used, ok := cgroupValue("/sys/fs/cgroup/memory.current")
	if !ok {
		return 0, false
	}
	// Most of what the cgroup holds can be the files' cache, which the
	// kernel gives back when a process needs memory: count it as free.
	data, _ := os.ReadFile("/sys/fs/cgroup/memory.stat")
	return limit - used + inactiveFile(string(data)), true
}

// inactiveFile reads the cache the kernel takes back first from a cgroup's
// memory.stat, or 0.
func inactiveFile(stat string) int64 {
	for line := range strings.Lines(stat) {
		if v, ok := strings.CutPrefix(line, "inactive_file "); ok {
			n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			return n
		}
	}
	return 0
}

// cgroupValue reads a number from a cgroup file; "max" is no limit.
func cgroupValue(path string) (int64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	return n, err == nil
}

// closedSession is what the bridge keeps of a session to resume it.
type closedSession struct {
	key, claudeID string
	seen          []string
	echo          string
	// why is the reason the process was closed.
	why string
	// exited is closed when the process has ended.
	exited <-chan struct{}
}

// maxClosed is how many closed sessions the bridge remembers.
const maxClosed = 20

// systemChanged is why a session cannot take a request as it is: claude
// takes both only when it starts.
const systemChanged = "system prompt or effort changed"

func (b *bridge) idleFor() time.Duration {
	if b.idle == 0 {
		return 5 * time.Minute
	}
	return b.idle
}

func (b *bridge) limit() int {
	if b.max == 0 {
		return 2
	}
	return b.max
}

// session is one claude process and what it has been told.
type session struct {
	id string
	// conv names fx's conversation and key what the process cannot change:
	// the model and the tools.
	conv, key string
	tools     []tool

	cmd    *exec.Cmd
	dir    string
	events chan event
	// exit says why claude stopped, once events is closed.
	exit string
	// done is closed when the process is told to stop or has stopped, and
	// exited when it has ended.
	done   chan struct{}
	exited chan struct{}
	once   sync.Once
	// resumed says that the process was started with --resume.
	resumed bool
	// system is the fingerprint of the system prompt the process runs with.
	system string

	// write guards stdin.
	write sync.Mutex
	stdin io.WriteCloser

	mu sync.Mutex
	// claudeID is claude's own name for the conversation, for --resume.
	claudeID string
	// busy says that a request is being answered, and lastUsed when the
	// session was last. The timer closes the session when it stays idle.
	busy     bool
	lastUsed time.Time
	timer    *time.Timer
	// seen are the fingerprints of fx's messages that claude knows.
	seen []string
	// emitted are the tool calls fx was asked to run, and echo is the
	// fingerprint of the assistant message that carried them.
	emitted []string
	echo    string
	// results hold each tool call's answer: for the MCP call that waits
	// for it, or for the one that has not come yet.
	results map[string]chan toolResult
}

// request is the part of fx's Chat Completions request the bridge reads.
type request struct {
	Model    string    `json:"model"`
	Effort   string    `json:"reasoning_effort"`
	Stream   bool      `json:"stream"`
	Messages []message `json:"messages"`
	Tools    []tool    `json:"tools"`
}

type message struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []toolCall      `json:"tool_calls"`
	ToolCallID string          `json:"tool_call_id"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type tool struct {
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

// text is the message's text, whether fx sent it whole or in parts.
func (m message) text() string {
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	json.Unmarshal(m.Content, &parts)
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// image is a picture fx sent, in base64.
type image struct {
	mediaType, data string
}

// images are the message's pictures, which fx sends as data URLs.
func (m message) images() []image {
	var parts []struct {
		Type     string `json:"type"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	json.Unmarshal(m.Content, &parts)
	var images []image
	for _, p := range parts {
		rest, ok := strings.CutPrefix(p.ImageURL.URL, "data:")
		if p.Type != "image_url" || !ok {
			continue
		}
		if mediaType, data, ok := strings.Cut(rest, ";base64,"); ok {
			images = append(images, image{mediaType, data})
		}
	}
	return images
}

// turn is what claude is told in one user turn.
type turn struct {
	text   string
	images []image
}

func (t turn) empty() bool { return t.text == "" && len(t.images) == 0 }

// toolResult is fx's answer to a tool call.
type toolResult struct {
	text   string
	images []image
}

// fingerprint identifies a message by what matters to the model.
func fingerprint(m message) string {
	calls := make([][3]string, len(m.ToolCalls))
	for i, c := range m.ToolCalls {
		// fx may send the arguments back spaced differently.
		args := c.Function.Arguments
		var compact bytes.Buffer
		if json.Compact(&compact, []byte(args)) == nil {
			args = compact.String()
		}
		calls[i] = [3]string{c.ID, c.Function.Name, args}
	}
	data, _ := json.Marshal(struct {
		Role, Content string
		Calls         [][3]string
		ID            string
	}{m.Role, m.text(), calls, m.ToolCallID})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// models are the names claude takes for a model.
var models = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Chat answers fx's Chat Completions request with claude. conv names
// fx's conversation.
func (c *Claude) Chat(w http.ResponseWriter, r *http.Request, body []byte, conv string) {
	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !models.MatchString(req.Model) {
		http.Error(w, fmt.Sprintf("%q is not a Claude model", req.Model), http.StatusBadRequest)
		return
	}
	token, err := c.Token()
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	var system []string
	var msgs []message
	for _, m := range req.Messages {
		if m.Role == "system" || m.Role == "developer" {
			system = append(system, m.text())
			continue
		}
		msgs = append(msgs, m)
	}
	if len(msgs) == 0 {
		http.Error(w, "the request has no messages", http.StatusBadRequest)
		return
	}
	prompt := strings.Join(system, "\n\n")
	fps := make([]string, len(msgs))
	for i, m := range msgs {
		fps[i] = fingerprint(m)
	}
	toolsJSON, _ := json.Marshal(req.Tools)
	sum := sha256.Sum256([]byte(req.Model + "\x00" + string(toolsJSON)))
	key := hex.EncodeToString(sum[:])

	rep := &reply{w: w, stream: req.Stream, id: "chatcmpl-" + randomHex(8), model: req.Model}

	if !slices.Contains(Efforts, req.Effort) {
		req.Effort = ""
	}
	s, t, why := c.resume(msgs, fps, key, conv, digest(req.Effort+"\x00"+prompt))
	if s == nil {
		// A process that holds calls for this conversation waits for
		// answers that will not come.
		c.interruptHeld(conv)

		var resume string
		closed, tail := c.reopen(msgs, fps, key)
		if closed != nil {
			// Two processes must not write the same transcript.
			select {
			case <-closed.exited:
			case <-time.After(10 * time.Second):
			}
			resume, t = closed.claudeID, joinUsers(tail)
			log.Printf("claude: resumed %s process for %d messages: %s", req.Model, len(msgs), closed.why)
		} else {
			// The reason shows whether fx's tool loops stay in one process.
			t = opening(msgs)
			log.Printf("claude: new %s process for %d messages: %s", req.Model, len(msgs), why)
		}

		var err error
		s, err = c.start(token, req, prompt, conv, key, resume)
		if errors.Is(err, errNoMemory) {
			w.Header().Set("Retry-After", "30")
			http.Error(w, err.Error(), http.StatusTooManyRequests)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		s.mu.Lock()
		s.seen = fps
		s.mu.Unlock()
	}
	if !t.empty() {
		if err := s.send(userTurn(t)); err != nil {
			s.stop()
			c.drop(s)
			http.Error(w, "claude stopped: "+s.exit, http.StatusBadGateway)
			return
		}
	}
	c.relay(r.Context(), s, rep)
}

// resume finds the live session that the request continues, and the user
// turn to send it, if any. A session continues in two ways: it waits for
// tool results, and the request is what it has seen, plus the assistant
// message that asked for the tools, plus one result for each tool it asked
// for; or it is idle, and the request is what it has seen, plus its last
// answer, plus new user messages. Without one, it says why the
// conversation's own session did not fit.
func (c *Claude) resume(msgs []message, fps []string, key, conv, system string) (*session, turn, string) {
	c.bridge.mu.Lock()
	defer c.bridge.mu.Unlock()
	why := "no session"
	for _, s := range c.bridge.sessions {
		t, reason := s.take(msgs, fps, key, system)
		if reason == "" {
			return s, t, ""
		}
		if reason == systemChanged {
			// Its history is still good: the process restarts with
			// the new prompt.
			c.retire(s, reason)
		}
		if s.conv == conv {
			why = reason
		}
	}
	return nil, turn{}, why
}

// take gives the request to s, with the user turn it adds, or says why it
// is not s's. The caller holds c.bridge.mu.
func (s *session) take(msgs []message, fps []string, key, system string) (turn, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.key != key:
		return turn{}, "model or tools changed"
	case s.busy:
		return turn{}, "busy"
	case len(s.emitted) > 0:
		return turn{}, s.takeResults(msgs, fps)
	}

	tail, reason := turnTail(msgs, fps, s.seen, s.echo)
	if reason != "" {
		return turn{}, reason
	}
	if s.system != system {
		return turn{}, systemChanged
	}
	s.claim(fps)
	s.echo = ""
	return joinUsers(tail), ""
}

// takeResults delivers the tool results to s, or says why they are not
// its. The caller holds s.mu.
func (s *session) takeResults(msgs []message, fps []string) string {
	// fx sends the images that tools returned in a user message after
	// the results; they go with the last result.
	end := len(msgs)
	var images []image
	if end > 1 && msgs[end-1].Role == "user" && msgs[end-2].Role == "tool" {
		if images = msgs[end-1].images(); len(images) > 0 {
			end--
		}
	}
	n := end
	for n > 0 && msgs[n-1].Role == "tool" {
		n--
	}
	want := append(slices.Clone(s.seen), s.echo)
	if !slices.Equal(fps[:n], want) {
		i := 0
		for i < min(n, len(want)) && fps[i] == want[i] {
			i++
		}
		return fmt.Sprintf("history differs at message %d (had %d, got %d before the results)", i, len(want), n)
	}
	var ids []string
	for _, m := range msgs[n:end] {
		ids = append(ids, m.ToolCallID)
	}
	if !slices.Equal(slices.Sorted(slices.Values(ids)), slices.Sorted(slices.Values(s.emitted))) {
		return "results for other calls"
	}

	for i, m := range msgs[n:end] {
		r := toolResult{text: m.text()}
		if n+i == end-1 {
			r.images = images
		}
		select {
		case s.slot(m.ToolCallID) <- r:
		default:
		}
	}
	s.claim(fps)
	return ""
}

// claim marks s as answering a request that has fps. The caller holds s.mu.
func (s *session) claim(fps []string) {
	s.seen, s.emitted, s.busy = fps, nil, true
	if s.timer != nil {
		s.timer.Stop()
	}
}

// turnTail is what a request adds after seen and the assistant message
// echo: user messages and nothing else.
func turnTail(msgs []message, fps, seen []string, echo string) ([]message, string) {
	want := append(slices.Clone(seen), echo)
	if len(fps) <= len(want) || !slices.Equal(fps[:len(want)], want) {
		i := 0
		for i < min(len(fps), len(want)) && fps[i] == want[i] {
			i++
		}
		return nil, fmt.Sprintf("history differs at message %d (had %d, got %d)", i, len(want), len(fps))
	}
	for _, m := range msgs[len(want):] {
		if m.Role != "user" {
			return nil, "the new messages are not from the user"
		}
	}
	return msgs[len(want):], ""
}

// joinUsers makes user messages one turn.
func joinUsers(msgs []message) turn {
	var t turn
	texts := make([]string, len(msgs))
	for i, m := range msgs {
		texts[i] = m.text()
		t.images = append(t.images, m.images()...)
	}
	t.text = strings.Join(texts, "\n\n")
	return t
}

// reopen takes the closed session that the request continues, with the user
// turn it adds.
func (c *Claude) reopen(msgs []message, fps []string, key string) (*closedSession, []message) {
	c.bridge.mu.Lock()
	defer c.bridge.mu.Unlock()
	for i, cs := range c.bridge.closed {
		if cs.key != key {
			continue
		}
		if tail, reason := turnTail(msgs, fps, cs.seen, cs.echo); reason == "" {
			c.bridge.closed = slices.Delete(c.bridge.closed, i, i+1)
			return cs, tail
		}
	}
	return nil, nil
}

// rest lets s close when no turn comes. It runs before fx sees the end of
// the reply, so the next request cannot come first.
func (c *Claude) rest(s *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer == nil {
		s.timer = time.AfterFunc(c.bridge.idleFor(), func() { c.closeIdle(s) })
		return
	}
	s.timer.Reset(c.bridge.idleFor())
}

// closeIdle closes s if it still has nothing to do.
func (c *Claude) closeIdle(s *session) {
	c.bridge.mu.Lock()
	defer c.bridge.mu.Unlock()
	s.mu.Lock()
	idle := !s.busy && len(s.emitted) == 0
	s.mu.Unlock()
	if idle {
		c.retire(s, "idle")
	}
}

// retire closes an idle session and remembers it, so that a later request
// can resume it. The caller holds c.bridge.mu.
func (c *Claude) retire(s *session, why string) {
	s.mu.Lock()
	s.busy = true
	cs := &closedSession{key: s.key, claudeID: s.claudeID, seen: s.seen, echo: s.echo, why: why, exited: s.exited}
	s.mu.Unlock()
	if why == "limit" {
		cs.why = "idle"
	}

	log.Printf("claude: closed process: %s", why)
	delete(c.bridge.sessions, s.id)
	if cs.claudeID != "" {
		c.bridge.closed = append(c.bridge.closed, cs)
		if len(c.bridge.closed) > maxClosed {
			c.forget(c.bridge.closed[0].claudeID)
			c.bridge.closed = c.bridge.closed[1:]
		}
	}
	s.shut()
}

// makeRoom closes the idle session used least recently when the limit of
// live processes is reached. Busy sessions are never refused for the count:
// an agent that holds a call may wait for its subagent's process. Past the
// limit, with none to close, a process starts only if memory is left.
func (c *Claude) makeRoom() error {
	c.bridge.mu.Lock()
	defer c.bridge.mu.Unlock()
	live := len(c.bridge.sessions)
	if live < c.bridge.limit() {
		return nil
	}

	var oldest *session
	var at time.Time
	for _, s := range c.bridge.sessions {
		s.mu.Lock()
		if !s.busy && len(s.emitted) == 0 && (oldest == nil || s.lastUsed.Before(at)) {
			oldest, at = s, s.lastUsed
		}
		s.mu.Unlock()
	}
	if oldest == nil {
		log.Printf("claude: %d processes live, none idle", live)

		free := c.bridge.free
		if free == nil {
			free = freeMemory
		}
		if n, ok := free(); ok && n < minFree {
			log.Printf("claude: no memory for another process (%d live)", live)
			return errNoMemory
		}
		return nil
	}
	c.retire(oldest, "limit")
	return nil
}

// slot is where the answer to a tool call goes, for the MCP call to take.
// The caller holds s.mu.
func (s *session) slot(id string) chan toolResult {
	if s.results[id] == nil {
		s.results[id] = make(chan toolResult, 1)
	}
	return s.results[id]
}

// emit notes the tool calls that fx will run, and the message that fx will
// send back to ask for them.
func (s *session) emit(rep *reply) {
	echo := message{Role: "assistant", ToolCalls: make([]toolCall, len(rep.calls))}
	echo.Content, _ = json.Marshal(rep.text.String())
	ids := make([]string, len(rep.calls))
	for i, c := range rep.calls {
		ids[i] = c.id
		echo.ToolCalls[i].ID = c.id
		echo.ToolCalls[i].Function.Name = c.name
		echo.ToolCalls[i].Function.Arguments = c.args.String()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.emitted, s.echo = ids, fingerprint(echo)
	s.free()
}

// free lets the next request take s. It must come before fx sees the end
// of the reply: fx answers a quick tool at once. The caller holds s.mu.
func (s *session) free() {
	s.busy = false
	s.lastUsed = time.Now()
}

// finish notes the end of a turn: fx will send back the answer, and
// the session waits for the next turn.
func (s *session) finish(rep *reply) {
	echo := message{Role: "assistant"}
	echo.Content, _ = json.Marshal(rep.text.String())

	s.mu.Lock()
	defer s.mu.Unlock()
	s.emitted, s.echo = nil, fingerprint(echo)
	s.free()
}

// opening is claude's first turn in a new process: the history it has not
// lived, in words, then the last user message.
func opening(msgs []message) turn {
	last := msgs[len(msgs)-1]
	tail := turn{text: "Continue from the last tool result."}
	if last.Role == "user" {
		tail = joinUsers(msgs[len(msgs)-1:])
		msgs = msgs[:len(msgs)-1]
	}
	if len(msgs) == 0 {
		return tail
	}

	var lines []string
	at := map[string]int{}
	for _, m := range msgs {
		switch m.Role {
		case "tool":
			if i, ok := at[m.ToolCallID]; ok {
				lines[i] += " -> result: " + m.text()
			}
		case "assistant":
			if t := m.text(); t != "" {
				lines = append(lines, "assistant: "+t)
			}
			for _, c := range m.ToolCalls {
				at[c.ID] = len(lines)
				lines = append(lines, fmt.Sprintf("assistant called tool %s(%s)", c.Function.Name, c.Function.Arguments))
			}
		default:
			lines = append(lines, m.Role+": "+m.text())
		}
	}
	tail.text = "<conversation-checkpoint>\n" + strings.Join(lines, "\n") + "\n</conversation-checkpoint>\n" + tail.text
	return tail
}

// userTurn is the line that gives claude a turn: its text, or Claude's
// content blocks when it has images.
func userTurn(t turn) []byte {
	var content any = t.text
	if len(t.images) > 0 {
		var blocks []any
		if t.text != "" {
			blocks = append(blocks, map[string]string{"type": "text", "text": t.text})
		}
		for _, img := range t.images {
			blocks = append(blocks, map[string]any{"type": "image", "source": map[string]string{
				"type": "base64", "media_type": img.mediaType, "data": img.data,
			}})
		}
		content = blocks
	}

	var line bytes.Buffer
	enc := json.NewEncoder(&line)
	enc.SetEscapeHTML(false)
	enc.Encode(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": content}})
	return bytes.TrimSpace(line.Bytes())
}

// start runs a new claude for a conversation.
func (c *Claude) start(token string, req request, system, conv, key, resume string) (*session, error) {
	if err := os.MkdirAll(ConfigDir(c.cfg), 0o700); err != nil {
		return nil, err
	}
	// Claude files its transcripts by working directory, so resuming
	// needs the same one every time.
	if err := os.MkdirAll(WorkDir(c.cfg), 0o700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "claude-")
	if err != nil {
		return nil, err
	}

	secret := c.secret()
	id := randomHex(16)
	mcp, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"den": map[string]any{
		"type":    "http",
		"url":     c.base + "/claude-mcp/" + id,
		"headers": map[string]string{"Authorization": "Bearer " + secret},
	}}})
	// Both go in files in the process's own folder, not in its arguments:
	// any user can read a process's arguments, the agent too, and the
	// secret would let it answer for fx. A long system prompt would also
	// pass Linux's limit for one argument.
	mcpFile, systemFile := filepath.Join(dir, "mcp.json"), filepath.Join(dir, "system.md")
	if err := os.WriteFile(mcpFile, mcp, 0o600); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	if err := os.WriteFile(systemFile, []byte(system), 0o600); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}

	bin := c.bin
	if bin == "" {
		bin = Path(c.cfg)
	}
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json",
		"--verbose", "--include-partial-messages", "--tools", "", "--strict-mcp-config",
		"--setting-sources", "", "--settings", `{"disableAllHooks":true}`,
		"--mcp-config", mcpFile, "--allowedTools", prefix + "*",
		"--system-prompt-file", systemFile, "--model", req.Model}
	if req.Effort != "" {
		args = append(args, "--effort", req.Effort)
	}
	if resume != "" {
		args = append(args, "--resume", resume)
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = WorkDir(c.cfg)
	cmd.Env = c.Env(token)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	s := &session{
		id: id, conv: conv, key: key, tools: req.Tools,
		cmd: cmd, dir: dir, stdin: stdin,
		events:  make(chan event, 256),
		done:    make(chan struct{}),
		exited:  make(chan struct{}),
		results: map[string]chan toolResult{},
		resumed: resume != "", system: digest(req.Effort + "\x00" + system),
		busy: true,
	}
	if err := c.makeRoom(); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	// Claude asks for the tools as soon as it starts.
	c.bridge.mu.Lock()
	if c.bridge.sessions == nil {
		c.bridge.sessions = map[string]*session{}
	}
	c.bridge.sessions[id] = s
	c.bridge.mu.Unlock()

	if err := cmd.Start(); err != nil {
		c.drop(s)
		os.RemoveAll(dir)
		return nil, fmt.Errorf("starting claude: %v", err)
	}

	go c.read(s, stdout, &stderr)
	return s, nil
}

// read turns claude's stdout into events until claude stops.
func (c *Claude) read(s *session, stdout io.Reader, stderr *bytes.Buffer) {
	defer close(s.exited)
	defer close(s.events)

	var buf bytes.Buffer
	chunk := make([]byte, 64<<10)
	for {
		n, err := stdout.Read(chunk)
		buf.Write(chunk[:n])
		for {
			i := bytes.IndexByte(buf.Bytes(), '\n')
			if i < 0 {
				break
			}
			line := buf.Next(i + 1)
			var ev event
			if json.Unmarshal(line, &ev) == nil {
				if ev.SessionID != "" {
					s.mu.Lock()
					s.claudeID = ev.SessionID
					s.mu.Unlock()
				}
				select {
				case s.events <- ev:
				case <-s.done:
				}
			}
		}
		if err != nil {
			break
		}
	}

	err := s.cmd.Wait()
	s.exit = clip(stderr.String())
	if s.exit == "" && err != nil {
		s.exit = err.Error()
	}
	if s.exit == "" {
		s.exit = "it exited"
	}
	s.shut()
	c.drop(s)
	os.RemoveAll(s.dir)
}

// send writes a line to claude.
func (s *session) send(line []byte) error {
	s.write.Lock()
	defer s.write.Unlock()
	_, err := s.stdin.Write(append(line, '\n'))
	return err
}

// stop tells claude to give up what it is doing and end.
func (s *session) stop() {
	s.send([]byte(`{"type":"control_request","request_id":"den","request":{"subtype":"interrupt"}}`))
	s.shut()
}

// shut closes claude's stdin, which ends it, and frees the MCP calls that
// wait for tool results. A claude that stays gets killed.
func (s *session) shut() {
	s.once.Do(func() {
		close(s.done)
		s.write.Lock()
		s.stdin.Close()
		s.write.Unlock()
		time.AfterFunc(10*time.Second, func() { s.cmd.Process.Kill() })
	})
}

func (c *Claude) drop(s *session) {
	c.bridge.mu.Lock()
	delete(c.bridge.sessions, s.id)
	c.bridge.mu.Unlock()
}

// interruptHeld stops the sessions of a conversation that wait for tool
// results.
func (c *Claude) interruptHeld(conv string) {
	c.bridge.mu.Lock()
	var held []*session
	for _, s := range c.bridge.sessions {
		s.mu.Lock()
		if s.conv == conv && len(s.emitted) > 0 {
			held = append(held, s)
		}
		s.mu.Unlock()
	}
	c.bridge.mu.Unlock()

	for _, s := range held {
		s.stop()
		c.drop(s)
	}
}

// secret is what the MCP endpoint takes as proof of being claude.
func (c *Claude) secret() string {
	c.bridge.mu.Lock()
	defer c.bridge.mu.Unlock()
	if c.bridge.secret == "" {
		c.bridge.secret = randomHex(32)
	}
	return c.bridge.secret
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// forget removes the transcript of a conversation the bridge can no longer
// resume: claude keeps it, with what the user said, until it is deleted.
func (c *Claude) forget(claudeID string) {
	if claudeID == "" || strings.ContainsAny(claudeID, `/\.`) {
		return
	}
	files, _ := filepath.Glob(filepath.Join(ConfigDir(c.cfg), "projects", "*", claudeID+".jsonl"))
	for _, f := range files {
		os.Remove(f)
		os.RemoveAll(strings.TrimSuffix(f, ".jsonl"))
	}
}
