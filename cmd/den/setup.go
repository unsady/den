package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// publishedImage is den's image for those who do not build it.
const publishedImage = "ghcr.io/unsady/den:latest"

// setup takes the user from nothing to a running den, and skips each step
// that is already done, so it also finishes or repairs a setup.
func setup(c hostConfig) error {
	e := c.engine()
	if _, err := exec.LookPath(string(e)); err != nil {
		if runtime.GOOS == "darwin" {
			return errors.New("den runs in a container: install Apple's container from github.com/apple/container/releases, or Docker, then run den setup again")
		}
		return errors.New("den runs in a container: install Docker or Podman, then run den setup again")
	}
	if e == appleContainer && !c.exists("system", "status") {
		if err := step("Starting Apple's container service", func(context.Context) error { return c.quiet("system", "start") }); err != nil {
			return err
		}
		done("Apple's container service is running")
	}

	_, saved := loadHost()
	if !saved {
		home, err := input("Folder for den's notes and files", c.Home)
		if err != nil {
			return err
		}
		c.Home = expand(home)
		if c.Source == "" {
			if wd, err := os.Getwd(); err == nil && isSource(wd) {
				c.Source = wd
			}
		}
		// Without a checkout to build from, den takes the published image.
		if c.Source == "" {
			c.Image = publishedImage
		}
		c.Runtime = string(e)
	}

	if !c.exists("image", "inspect", c.Image) {
		if err := pullOrBuild(c); err != nil {
			return err
		}
	}
	if err := c.save(); err != nil {
		return err
	}
	if err := launch(c, "Starting den", start); err != nil {
		return err
	}

	have := secrets(c)
	if !have["telegram_token"] || !have["telegram_chat_id"] {
		if err := setupTelegram(c); err != nil {
			return err
		}
	}

	if !hasProvider(have) {
		if err := chooseAccount(c); err != nil {
			return err
		}
	}

	fmt.Println()
	done("den is ready. Write to your bot in Telegram.")
	fmt.Printf(`
  den connect mail      read and send mail with an app password
  den connect google    Gmail and Calendar through your own Google client
  den status            what den runs and which secrets it has
  den update            rebuild and restart; notes and secrets stay

Settings are in %s.
`, configPath())
	return nil
}

// hasProvider reports whether den has a model to use: a provider's key, or
// a ChatGPT subscription.
func hasProvider(have map[string]bool) bool {
	if have["codex"] {
		return true
	}
	for name := range have {
		if strings.HasPrefix(name, "provider_") {
			return true
		}
	}
	return false
}

// setupTelegram checks the bot's token and finds the user's chat with it
// from the first message they send, so they never look up an id.
func setupTelegram(c hostConfig) error {
	fmt.Println()
	note("den talks to you through a Telegram bot. Create one with @BotFather and copy its token.")
	var token, bot string
	for {
		var err error
		if token, err = ask("Bot token", true); err != nil {
			return err
		}
		var me struct {
			Username string `json:"username"`
		}
		err = step("Checking the token", func(context.Context) error { return telegram(token, "getMe", nil, &me) })
		if err == nil {
			bot = me.Username
			break
		}
		if !terminal() {
			return fmt.Errorf("Telegram does not accept this token: %v", err)
		}
		failed("Telegram does not accept this token: %v", err)
	}
	done("Bot @%s", bot)

	var name string
	err := step("Send any message to @"+bot+" in Telegram", func(ctx context.Context) error {
		var err error
		name, err = firstMessage(ctx, c, token)
		return err
	})
	if err != nil {
		return err
	}
	done("Connected to your chat, %s", name)
	return nil
}

// firstMessage waits for the user's first message to the bot and keeps
// its chat and the token in den. It returns the user's name.
func firstMessage(ctx context.Context, c hostConfig, token string) (string, error) {
	var offset int64
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		var updates []struct {
			ID      int64 `json:"update_id"`
			Message *struct {
				Chat struct {
					ID int64 `json:"id"`
				} `json:"chat"`
				From struct {
					Name string `json:"first_name"`
				} `json:"from"`
			} `json:"message"`
		}
		if err := telegram(token, "getUpdates", url.Values{"offset": {fmt.Sprint(offset)}, "timeout": {"20"}}, &updates); err != nil {
			return "", err
		}
		for _, u := range updates {
			offset = u.ID + 1
			if u.Message == nil {
				continue
			}
			// Mark the message read, so den does not answer it as a request.
			telegram(token, "getUpdates", url.Values{"offset": {fmt.Sprint(offset)}, "timeout": {"0"}}, nil)

			if err := insideQuiet(c, fmt.Sprint(u.Message.Chat.ID), "secret", "set", "telegram_chat_id"); err != nil {
				return "", err
			}
			if err := insideQuiet(c, token, "secret", "set", "telegram_token"); err != nil {
				return "", err
			}
			return u.Message.From.Name, nil
		}
	}
	return "", errors.New("no message came in 5 minutes: run den setup again")
}

func telegram(token, method string, params url.Values, result any) error {
	client := &http.Client{Timeout: time.Minute}
	resp, err := client.PostForm("https://api.telegram.org/bot"+token+"/"+method, params)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var body struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return err
	}
	if !body.OK {
		return errors.New(body.Description)
	}
	if result != nil {
		return json.Unmarshal(body.Result, result)
	}
	return nil
}

func isSource(dir string) bool {
	_, err1 := os.Stat(filepath.Join(dir, "Dockerfile"))
	_, err2 := os.Stat(filepath.Join(dir, "cmd", "den"))
	return err1 == nil && err2 == nil
}

func expand(path string) string {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, rest)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}
