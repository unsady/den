package gitproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/unsady/den/internal/approve"
)

// A push that waits for the user goes to GitHub at once, under refs of
// den's own that no branch, tag, or workflow reads. GitHub then says what
// the commits are, so the question names them and not only their IDs, and
// once the user allows the push den moves the branches to them. den
// deletes its refs whatever the answer.

// heldRefs is where den keeps the commits of a push that waits.
const heldRefs = "refs/den/held/"

// shownCommits is how many commits of a branch a question names.
const shownCommits = 8

// held is a push GitHub keeps under den's refs.
type held struct {
	p           *Proxy
	owner, repo string
	commands    []command
	// refs are den's refs, one for each command.
	refs []string
	// forced are the commands that rewrite their branch's history.
	forced []bool
	// shown says what each command does, for the question.
	shown []string
}

// keep sends a push that changes or creates branches to GitHub under den's
// refs and reads from GitHub what it holds. It returns nil for any other
// push, such as one that deletes a branch or pushes a tag.
func (p *Proxy) keep(owner, repo string, header http.Header, data []byte, commands []command) (*held, error) {
	if p.api == "" {
		return nil, nil
	}
	for _, c := range commands {
		if !strings.HasPrefix(c.ref, "refs/heads/") || zero(c.new) {
			return nil, nil
		}
	}

	h := &held{p: p, owner: owner, repo: repo, commands: commands}
	id := strings.ToLower(rand.Text())
	for i := range commands {
		h.refs = append(h.refs, heldRefs+id+"/"+strconv.Itoa(i))
	}
	body, err := rehome(header, data, h.refs)
	if err != nil {
		return nil, err
	}
	header = header.Clone()
	header.Del("Content-Encoding")
	took, refused, err := p.replay(owner, repo, header, body)
	switch {
	case err != nil:
		return nil, err
	case refused != "":
		h.drop()
		return nil, errors.New(refused)
	}
	for _, ref := range h.refs {
		if !took[ref] {
			h.drop()
			return nil, fmt.Errorf("GitHub did not take %s", ref)
		}
	}

	for _, c := range commands {
		line, forced := h.describe(c)
		h.shown = append(h.shown, line)
		h.forced = append(h.forced, forced)
	}
	return h, nil
}

// rehome rewrites the commands at the start of a push so each creates one
// of refs instead of changing its branch. The objects after them stay as
// they are.
func rehome(header http.Header, data []byte, refs []string) ([]byte, error) {
	if header.Get("Content-Encoding") == "gzip" {
		z, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		if data, err = io.ReadAll(io.LimitReader(z, maxHeld)); err != nil {
			return nil, err
		}
	}

	var out bytes.Buffer
	n := 0
	for {
		if len(data) < 4 {
			return nil, errors.New("the push ends before its commands do")
		}
		size, err := strconv.ParseUint(string(data[:4]), 16, 16)
		if err != nil || size != 0 && size < 4 || int(size) > len(data) {
			return nil, errors.New("the push has a line den cannot read")
		}
		if size == 0 {
			out.Write(data)
			break
		}
		line := string(data[4:size])
		data = data[size:]
		text, caps, hasCaps := strings.Cut(strings.TrimSuffix(line, "\n"), "\x00")
		if strings.HasPrefix(text, "shallow ") {
			out.WriteString(pkt(line))
			continue
		}
		c, ok := parseCommand(text)
		if !ok || n >= len(refs) {
			return nil, errors.New("the push has a command den cannot read")
		}
		text = strings.Repeat("0", len(c.new)) + " " + c.new + " " + refs[n]
		if hasCaps {
			text += "\x00" + caps
		}
		out.WriteString(pkt(text + "\n"))
		n++
	}
	if n != len(refs) {
		return nil, errors.New("the push has fewer commands than den read")
	}
	return out.Bytes(), nil
}

// describe says what c does to its branch, from GitHub's comparison of the
// branch with the new commit, and whether it rewrites the branch's history.
// When GitHub does not say, it names the commit IDs, and takes the push for
// one that may rewrite.
func (h *held) describe(c command) (string, bool) {
	if zero(c.old) {
		return describe(c), false
	}
	name := strings.TrimPrefix(c.ref, "refs/heads/")
	var cmp struct {
		Status  string `json:"status"`
		Ahead   int    `json:"ahead_by"`
		Behind  int    `json:"behind_by"`
		Commits []struct {
			Commit struct {
				Message string `json:"message"`
			} `json:"commit"`
		} `json:"commits"`
		Files []struct {
			Name string `json:"filename"`
		} `json:"files"`
	}
	path := "/compare/" + c.old + "..." + c.new
	if err := h.call(http.MethodGet, path, nil, &cmp); err != nil {
		log.Printf("git: %s/%s: %v", h.owner, h.repo, err)
		return describe(c), true
	}

	forced := cmp.Status != "ahead" && cmp.Status != "identical"
	line := fmt.Sprintf("• %s: %s", name, count(cmp.Ahead, "commit"))
	if n := len(cmp.Files); n > 0 {
		line += ", " + count(n, "file")
	}
	if forced {
		line += fmt.Sprintf(", and rewrites its history: drops %s", count(cmp.Behind, "commit"))
	}
	// GitHub lists the oldest first; the newest say most about a push.
	first := max(0, len(cmp.Commits)-shownCommits)
	for i := len(cmp.Commits) - 1; i >= first; i-- {
		title, _, _ := strings.Cut(cmp.Commits[i].Commit.Message, "\n")
		line += "\n   " + clip(title, 72)
	}
	if more := cmp.Ahead - (len(cmp.Commits) - first); more > 0 {
		line += fmt.Sprintf("\n   …and %d more", more)
	}
	return line + "\n   https://github.com/" + h.owner + "/" + h.repo + path, forced
}

func count(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

func clip(text string, n int) string {
	if runes := []rune(text); len(runes) > n {
		return string(runes[:n-1]) + "…"
	}
	return text
}

// move points each branch at the commit GitHub keeps for it. A branch that
// moved since the user saw the question stays as it is, as GitHub would
// refuse git itself. It returns the branches it moved and why it left the
// others.
func (h *held) move() (map[string]bool, []string) {
	took := map[string]bool{}
	var refused []string
	for i, c := range h.commands {
		name := strings.TrimPrefix(c.ref, "refs/heads/")
		var err error
		if zero(c.old) {
			err = h.call(http.MethodPost, "/git/refs", map[string]any{"ref": c.ref, "sha": c.new}, nil)
		} else {
			var now struct {
				Object struct {
					SHA string `json:"sha"`
				} `json:"object"`
			}
			if err = h.call(http.MethodGet, "/git/ref/heads/"+name, nil, &now); err == nil && now.Object.SHA != c.old {
				err = errors.New("the branch moved meanwhile")
			}
			if err == nil {
				err = h.call(http.MethodPatch, "/git/refs/heads/"+name, map[string]any{"sha": c.new, "force": h.forced[i]}, nil)
			}
		}
		if err != nil {
			refused = append(refused, name+": "+err.Error())
			continue
		}
		took[c.ref] = true
	}
	return took, refused
}

// drop deletes den's refs. The commits a branch does not hold by then go
// with them.
func (h *held) drop() {
	for _, ref := range h.refs {
		if err := h.call(http.MethodDelete, "/git/"+ref, nil, nil); err != nil {
			log.Printf("git: %s/%s: %s stays: %v", h.owner, h.repo, ref, err)
		}
	}
}

// call sends one request to the repository's API with the user's token and
// reads its answer into out when out is not nil.
func (h *held) call(method, path string, body, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var data io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		data = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.p.api+"/repos/"+h.owner+"/"+h.repo+path, data)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token := h.p.token(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Transport: h.p.transport}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var failed struct {
			Message string `json:"message"`
		}
		json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&failed)
		return fmt.Errorf("GitHub answered %d: %s", resp.StatusCode, failed.Message)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

// moveLater moves the branches of a kept push once the user answered, and
// reports what came of it, as pushLater does for a push den kept itself.
func (p *Proxy) moveLater(h *held, what, session string, answer error) {
	defer h.drop()
	report := func(agent, user string) {
		if p.Report != nil {
			p.Report(session, agent, user)
		}
	}
	if answer != nil {
		log.Printf("git: %s: not pushed: %v", what, answer)
		report(fmt.Sprintf("[den: %s was not pushed: %v.]", what, answer), "")
		return
	}

	took, refused := h.move()
	p.record(h.owner+"/"+h.repo, h.commands, took)
	if len(refused) > 0 {
		why := strings.Join(refused, "; ")
		log.Printf("git: %s: refused: %s", what, why)
		report(fmt.Sprintf("[den: the user allowed %s, and GitHub refused it: %s. When a branch moved meanwhile, fetch, rebase or merge, and push again.]", what, why),
			fmt.Sprintf("%s: GitHub refused it: %s", what, why))
		return
	}
	log.Printf("git: %s: pushed, allowed by the user", what)
	report(fmt.Sprintf("[den: the user allowed %s, and den pushed it. git fetch updates your copy of the remote branches. %s]", what, approve.Done), what+": pushed")
}
