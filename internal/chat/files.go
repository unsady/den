package chat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/unsady/den/internal/notify"
	"github.com/unsady/den/internal/proc"
	"github.com/unsady/den/internal/transcribe"
)

// Photos, voice messages, and files the user sends.

// receiveFile saves a photo or document and queues a message that names it.
func (c *Chat) receiveFile(ctx context.Context, telegram *notify.Telegram, m notify.Message) {
	if m.File.Size > notify.MaxDownload {
		c.send(ctx, telegram, "Could not save the file: "+notify.ErrTooLarge.Error()+".")
		return
	}
	path, data, err := c.saveFile(ctx, telegram, m)
	if err != nil {
		log.Printf("chat: file: %v", err)
		c.send(ctx, telegram, "Could not save the file: "+err.Error()+".")
		return
	}

	// The user's words come first and den's note about the file after them.
	var e entry
	switch {
	case m.File.Photo:
		e = entry{text: "[The user sent a photo, attached and saved at " + path + "]", image: path}
	case m.File.Voice:
		e.text = c.voiceText(ctx, path, data)
	default:
		e.text = "[The user sent a file, saved at " + path + "]"
	}
	if caption := strings.TrimSpace(m.Text); caption != "" {
		e.text = caption + "\n" + e.text
	}
	e.text = quoted(m, e.text)
	c.queue(ctx, telegram, m, e)
}

// voiceText describes a voice message saved at path with its transcript, or
// says why there is none.
func (c *Chat) voiceText(ctx context.Context, path string, data []byte) string {
	text, err := transcribe.Text(ctx, c.cfg, path, data)
	switch {
	case errors.Is(err, transcribe.ErrNotConfigured):
		return "[The user sent a voice message, saved at " + path + ". den has no speech-to-text set up, so there is no transcript.]"
	case err != nil:
		log.Printf("chat: transcribe: %v", err)
		return "[The user sent a voice message, saved at " + path + ". Transcribing it failed: " + err.Error() + "]"
	case text == "":
		return "[The user sent a voice message, saved at " + path + ". The transcript is empty.]"
	}
	return text + "\n[The user said this in a voice message, saved at " + path + "; the text is its transcript.]"
}

// saveFile downloads the file into the day's inbox folder and returns its
// path and contents.
// The agent writes the file, so a link the agent planted in the inbox cannot
// make den write where the agent cannot.
func (c *Chat) saveFile(ctx context.Context, telegram *notify.Telegram, m notify.Message) (string, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	var data bytes.Buffer
	if err := telegram.Download(ctx, m.File.ID, &data); err != nil {
		return "", nil, err
	}
	contents := data.Bytes()

	dir := filepath.Join(c.cfg.Inbox, time.Now().Format("2006-01-02"))
	name := fmt.Sprintf("%d-%s", m.ID, fileName(m.File))
	cmd, err := proc.Command(ctx, c.cfg, "/bin/sh", "-c", `set -C; umask 077; mkdir -p -- "$1" && cat > "$1/$2"`, "save", dir, name)
	if err != nil {
		return "", nil, err
	}
	cmd.Stdin = bytes.NewReader(contents)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", nil, fmt.Errorf("writing %s: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	return filepath.Join(dir, name), contents, nil
}

// fileName keeps a document's name readable and safe as one path element.
func fileName(f *notify.File) string {
	if f.Photo {
		return "photo.jpg"
	}
	if f.Voice {
		return "voice.ogg"
	}
	var b strings.Builder
	for _, r := range filepath.Base(f.Name) {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	name := strings.TrimLeft(b.String(), ".")
	if runes := []rune(name); len(runes) > 100 {
		name = string(runes[len(runes)-100:])
	}
	if name == "" {
		return "file"
	}
	return name
}
