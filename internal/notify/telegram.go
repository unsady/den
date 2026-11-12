// Package notify talks to the user through a Telegram bot.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/unsady/den/internal/config"
)

// ErrNotConfigured means no Telegram token and chat id were found.
var ErrNotConfigured = errors.New("telegram is not configured")

// maxMessage stays under Telegram's 4096-character message limit.
const maxMessage = 4000

// API is the Telegram Bot API address. Tests point it at a local server.
var API = "https://api.telegram.org"

type Telegram struct {
	token  string
	chatID string
	client *http.Client
}

// Load reads the bot token and chat id from files in the secrets directory.
// It returns ErrNotConfigured when either is missing. Secrets are never read
// from the environment: every process started in a container inherits it.
func Load(cfg config.Config) (*Telegram, error) {
	token := secret(cfg, "telegram_token")
	chatID := secret(cfg, "telegram_chat_id")
	if token == "" || chatID == "" {
		return nil, ErrNotConfigured
	}
	return &Telegram{token: token, chatID: chatID, client: &http.Client{}}, nil
}

// Send sends text to the user's chat, split into several messages when long,
// with its ``` blocks and `inline code` shown as code that a tap copies.
func (t *Telegram) Send(ctx context.Context, text string) error {
	_, err := t.SendIDs(ctx, text)
	return err
}

// SendIDs is Send that returns the ids of the messages it sent, so a reply
// to any of them can be told apart.
func (t *Telegram) SendIDs(ctx context.Context, text string) ([]int64, error) {
	var ids []int64
	for _, chunk := range split(strings.TrimSpace(text), maxMessage) {
		chunk, entities := code(chunk)
		body := map[string]any{
			"chat_id":                  t.chatID,
			"text":                     chunk,
			"disable_web_page_preview": true,
		}
		if len(entities) > 0 {
			body["entities"] = entities
		}
		var sent struct {
			ID int64 `json:"message_id"`
		}
		if err := t.call(ctx, "sendMessage", body, &sent); err != nil {
			return ids, err
		}
		ids = append(ids, sent.ID)
	}
	return ids, nil
}

// Post sends text as one message, cut to Telegram's limit, and returns its
// id for Edit and Delete. It arrives without a sound, since it only tells
// how a turn goes; the reply after it is the one to hear.
func (t *Telegram) Post(ctx context.Context, text string) (int64, error) {
	var sent struct {
		ID int64 `json:"message_id"`
	}
	err := t.call(ctx, "sendMessage", map[string]any{
		"chat_id":                  t.chatID,
		"text":                     clip(text),
		"disable_web_page_preview": true,
		"disable_notification":     true,
	}, &sent)
	return sent.ID, err
}

// Edit replaces the text of the message id that Post returned.
func (t *Telegram) Edit(ctx context.Context, id int64, text string) error {
	return t.call(ctx, "editMessageText", map[string]any{
		"chat_id":                  t.chatID,
		"message_id":               id,
		"text":                     clip(text),
		"disable_web_page_preview": true,
	}, nil)
}

// Delete removes the message id that Post returned.
func (t *Telegram) Delete(ctx context.Context, id int64) error {
	return t.call(ctx, "deleteMessage", map[string]any{"chat_id": t.chatID, "message_id": id}, nil)
}

// Fits says whether text goes in one message, uncut.
func Fits(text string) bool {
	return len(split(strings.TrimSpace(text), maxMessage)) <= 1
}

func clip(text string) string {
	if parts := split(strings.TrimSpace(text), maxMessage); len(parts) > 0 {
		return parts[0]
	}
	return ""
}

// Button is one inline button under a message. Data comes back in the
// Callback of the Message that reports the press.
type Button struct {
	Text string `json:"text"`
	Data string `json:"callback_data,omitempty"`
	// URL makes a button that opens a link instead.
	URL string `json:"url,omitempty"`
}

// SendButtons sends text with rows of buttons under it and returns the
// message's id.
func (t *Telegram) SendButtons(ctx context.Context, text string, rows [][]Button) (int64, error) {
	var sent struct {
		ID int64 `json:"message_id"`
	}
	err := t.call(ctx, "sendMessage", map[string]any{
		"chat_id":                  t.chatID,
		"text":                     clip(text),
		"disable_web_page_preview": true,
		"reply_markup":             map[string]any{"inline_keyboard": rows},
	}, &sent)
	return sent.ID, err
}

// SendHTML sends text in Telegram's HTML format, with rows of buttons under
// it when rows is not nil, and returns the message's id. The caller escapes
// what text quotes.
func (t *Telegram) SendHTML(ctx context.Context, text string, rows [][]Button) (int64, error) {
	var sent struct {
		ID int64 `json:"message_id"`
	}
	body := map[string]any{
		"chat_id":                  t.chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	if rows != nil {
		body["reply_markup"] = map[string]any{"inline_keyboard": rows}
	}
	err := t.call(ctx, "sendMessage", body, &sent)
	return sent.ID, err
}

// EditHTML replaces message id with text in Telegram's HTML format and the
// buttons under it with rows, removing them when rows is nil.
func (t *Telegram) EditHTML(ctx context.Context, id int64, text string, rows [][]Button) error {
	body := map[string]any{
		"chat_id":                  t.chatID,
		"message_id":               id,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	if rows != nil {
		body["reply_markup"] = map[string]any{"inline_keyboard": rows}
	}
	return t.call(ctx, "editMessageText", body, nil)
}

// React puts an emoji reaction on message id.
func (t *Telegram) React(ctx context.Context, id int64, emoji string) error {
	return t.call(ctx, "setMessageReaction", map[string]any{
		"chat_id":    t.chatID,
		"message_id": id,
		"reaction":   []map[string]string{{"type": "emoji", "emoji": emoji}},
	}, nil)
}

// Answer stops the spinner on a pressed button, showing text briefly if set.
func (t *Telegram) Answer(ctx context.Context, callback, text string) error {
	return t.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": callback, "text": text}, nil)
}

// MaxUpload is the largest file the Bot API lets a bot send.
const MaxUpload = 50 << 20

// maxPhoto is the largest image Telegram takes as a photo.
const maxPhoto = 10 << 20

// maxCaption stays under Telegram's 1024-character caption limit.
const maxCaption = 1000

// SendFile sends data as a file called name, or as a photo when it is a
// common image Telegram can show. A photo Telegram refuses, such as a very
// long screenshot, goes as a file instead.
func (t *Telegram) SendFile(ctx context.Context, name string, data []byte, caption string) error {
	if len(data) > MaxUpload {
		return errors.New("the file is over Telegram's 50 MB limit for bots")
	}
	if parts := split(strings.TrimSpace(caption), maxCaption); len(parts) > 0 {
		caption = parts[0]
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".webp":
		if len(data) <= maxPhoto && t.upload(ctx, "sendPhoto", "photo", name, data, caption) == nil {
			return nil
		}
	}
	return t.upload(ctx, "sendDocument", "document", name, data, caption)
}

func (t *Telegram) upload(ctx context.Context, method, field, name string, data []byte, caption string) error {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	_ = form.WriteField("chat_id", t.chatID)
	if caption != "" {
		_ = form.WriteField("caption", caption)
	}
	part, _ := form.CreateFormFile(field, name)
	_, _ = part.Write(data)
	_ = form.Close()
	return t.post(ctx, method, form.FormDataContentType(), &body, nil)
}

// Typing shows "typing…" in the user's chat for about five seconds.
func (t *Telegram) Typing(ctx context.Context) error {
	return t.call(ctx, "sendChatAction", map[string]any{"chat_id": t.chatID, "action": "typing"}, nil)
}

// Command is a bot command shown in Telegram's command menu.
type Command struct {
	Name        string `json:"command"`
	Description string `json:"description"`
}

// SetCommands replaces the bot's command menu.
func (t *Telegram) SetCommands(ctx context.Context, commands []Command) error {
	return t.call(ctx, "setMyCommands", map[string]any{"commands": commands}, nil)
}

// MaxDownload is the largest file the Bot API lets a bot download.
const MaxDownload = 20 << 20

// ErrTooLarge means a file is over MaxDownload.
var ErrTooLarge = errors.New("the file is over Telegram's 20 MB limit for bots")

// Message is an incoming message from the user's chat, or a press of one of
// the bot's buttons there.
type Message struct {
	ID int64
	// Text is the message text, or the caption of a photo or file.
	Text string
	// Quote is the part of an earlier message this one replies to, cut to a
	// few hundred characters, and QuotesBot says whether that message was
	// the bot's.
	Quote     string
	QuotesBot bool
	// ReplyTo is the id of the message this one replies to, or 0.
	ReplyTo int64
	// Callback is set for a button press instead of a message.
	Callback *Callback
	// File is the photo or document the message carries, if any.
	File *File
	// Other is true for a message den cannot read, such as a sticker or a
	// voice message.
	Other bool
}

// Callback is a press of a button sent with SendButtons.
type Callback struct {
	ID   string
	Data string
	// Message is the id of the message the button is under.
	Message int64
}

// maxQuote is how much of a replied-to message a Message carries.
const maxQuote = 300

// File is a photo, voice message, or document to fetch with Download.
type File struct {
	ID string
	// Name is the document's file name, empty for a photo or voice message.
	Name  string
	Size  int64
	Photo bool
	Voice bool
}

// Updates waits up to wait seconds for updates after offset and returns the
// messages from the user's chat with the offset to pass next. Messages from
// any other chat are dropped and reported in strangers.
func (t *Telegram) Updates(ctx context.Context, offset int64, wait int) (messages []Message, next int64, strangers []string, err error) {
	var updates []struct {
		ID       int64     `json:"update_id"`
		Message  *incoming `json:"message"`
		Callback *struct {
			ID      string    `json:"id"`
			Data    string    `json:"data"`
			Message *incoming `json:"message"`
		} `json:"callback_query"`
	}

	err = t.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         wait,
		"allowed_updates": []string{"message", "callback_query"},
	}, &updates)
	if err != nil {
		return nil, offset, nil, err
	}

	next = offset
	for _, u := range updates {
		next = max(next, u.ID+1)
		if c := u.Callback; c != nil {
			// A button's message is the bot's own, in the chat it was sent to.
			if c.Message == nil || strconv.FormatInt(c.Message.Chat.ID, 10) != t.chatID {
				strangers = append(strangers, "a button press in another chat")
				continue
			}
			messages = append(messages, Message{Callback: &Callback{ID: c.ID, Data: c.Data, Message: c.Message.ID}})
			continue
		}
		m := u.Message
		if m == nil {
			continue
		}
		if strconv.FormatInt(m.Chat.ID, 10) != t.chatID {
			strangers = append(strangers, fmt.Sprintf("chat %d (@%s)", m.Chat.ID, m.From.Username))
			continue
		}
		msg := Message{ID: m.ID, Text: m.Text}
		if r := m.Reply; r != nil {
			quote := r.Text
			if quote == "" {
				quote = r.Caption
			}
			if m.Quote != nil && m.Quote.Text != "" {
				quote = m.Quote.Text
			}
			if runes := []rune(strings.TrimSpace(quote)); len(runes) > maxQuote {
				quote = string(runes[:maxQuote]) + "…"
			}
			msg.Quote = strings.TrimSpace(quote)
			msg.QuotesBot = r.From.IsBot
			msg.ReplyTo = r.ID
		}
		switch {
		case len(m.Photo) > 0:
			// Telegram lists a photo's sizes from smallest to largest.
			p := m.Photo[len(m.Photo)-1]
			msg.File = &File{ID: p.ID, Size: p.Size, Photo: true}
			msg.Text = m.Caption
		case m.Document != nil:
			msg.File = &File{ID: m.Document.ID, Name: m.Document.Name, Size: m.Document.Size}
			msg.Text = m.Caption
		case m.Audio != nil:
			msg.File = &File{ID: m.Audio.ID, Name: m.Audio.Name, Size: m.Audio.Size}
			msg.Text = m.Caption
		case m.Voice != nil:
			msg.File = &File{ID: m.Voice.ID, Size: m.Voice.Size, Voice: true}
			msg.Text = m.Caption
		case m.Text == "":
			msg.Other = true
		}
		messages = append(messages, msg)
	}
	return messages, next, strangers, nil
}

// incoming is a message as Telegram sends it.
type incoming struct {
	Chat struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	From struct {
		Username string `json:"username"`
	} `json:"from"`
	ID      int64  `json:"message_id"`
	Text    string `json:"text"`
	Caption string `json:"caption"`
	Photo   []struct {
		ID   string `json:"file_id"`
		Size int64  `json:"file_size"`
	} `json:"photo"`
	Document *document `json:"document"`
	Audio    *document `json:"audio"`
	Voice    *document `json:"voice"`
	Reply    *struct {
		ID      int64  `json:"message_id"`
		Text    string `json:"text"`
		Caption string `json:"caption"`
		From    struct {
			IsBot bool `json:"is_bot"`
		} `json:"from"`
	} `json:"reply_to_message"`
	// Quote is the part of the replied-to message the user selected.
	Quote *struct {
		Text string `json:"text"`
	} `json:"quote"`
}

type document struct {
	ID   string `json:"file_id"`
	Name string `json:"file_name"`
	Size int64  `json:"file_size"`
}

// Download writes the file id to w.
func (t *Telegram) Download(ctx context.Context, id string, w io.Writer) error {
	var file struct {
		Path string `json:"file_path"`
	}
	if err := t.call(ctx, "getFile", map[string]any{"file_id": id}, &file); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, API+"/file/bot"+t.token+"/"+file.Path, nil)
	if err != nil {
		return errors.New("telegram: bad file path")
	}
	resp, err := t.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// The error text includes the URL, and the URL includes the token.
		return errors.New("telegram: download failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram: download failed (HTTP %d)", resp.StatusCode)
	}

	n, err := io.Copy(w, io.LimitReader(resp.Body, MaxDownload+1))
	if err != nil {
		return errors.New("telegram: download failed")
	}
	if n > MaxDownload {
		return ErrTooLarge
	}
	return nil
}

func (t *Telegram) call(ctx context.Context, method string, body any, result any) error {
	data, _ := json.Marshal(body)
	return t.post(ctx, method, "application/json", bytes.NewReader(data), result)
}

func (t *Telegram) post(ctx context.Context, method, contentType string, body io.Reader, result any) error {
	url := API + "/bot" + t.token + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return errors.New("telegram: bad request")
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := t.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// The error text includes the URL, and the URL includes the token.
		return fmt.Errorf("telegram: %s failed", method)
	}
	defer resp.Body.Close()

	var reply struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&reply)
	if !reply.OK {
		return fmt.Errorf("telegram: %s: %s (HTTP %d)", method, reply.Description, resp.StatusCode)
	}
	if result != nil {
		return json.Unmarshal(reply.Result, result)
	}
	return nil
}

func secret(cfg config.Config, file string) string {
	data, err := os.ReadFile(filepath.Join(cfg.Secrets, file))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// split cuts text into pieces of at most limit runes, preferring line breaks.
func split(text string, limit int) []string {
	var out []string
	runes := []rune(text)
	for len(runes) > limit {
		cut := limit
		for i := limit; i > limit/2; i-- {
			if runes[i] == '\n' {
				cut = i
				break
			}
		}
		out = append(out, strings.TrimSpace(string(runes[:cut])))
		runes = runes[cut:]
	}
	if len(runes) > 0 {
		out = append(out, string(runes))
	}
	return out
}
