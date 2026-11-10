package main

import (
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
		fmt.Println("Starting Apple's container service…")
		if err := c.run("system", "start"); err != nil {
			return err
		}
	}

	_, saved := loadHost()
	if !saved {
		home, err := ask(fmt.Sprintf("Folder for den's notes and files [%s]: ", c.Home), false)
		if err == nil && home != "" {
			c.Home = expand(home)
		}
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
		if err := getImage(c); err != nil {
			return err
		}
	}
	if err := c.save(); err != nil {
		return err
	}
	if err := start(c); err != nil {
		return err
	}

	have := secrets(c)
	if !have["telegram_token"] || !have["telegram_chat_id"] {
		if err := setupTelegram(c); err != nil {
			return err
		}
	}

	if !hasProvider(have) {
		fmt.Println("\nden needs a model. Any provider on models.dev works, such as opencode-go, opencode, deepseek, or openrouter.")
		provider, err := ask("Provider [opencode-go]: ", false)
		if err != nil {
			return err
		}
		if provider == "" {
			provider = "opencode-go"
		}
		if err := login(c, provider, "", ""); err != nil {
			return err
		}
	}

	fmt.Printf(`
den is ready. Write to your bot in Telegram.

  den connect mail      read and send mail with an app password
  den connect google    Gmail and Calendar through your own Google client
  den status            what den runs and which secrets it has
  den update            rebuild and restart; notes and secrets stay

Settings are in %s.
`, configPath())
	return nil
}

func hasProvider(have map[string]bool) bool {
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
	fmt.Println("\nden talks to you through a Telegram bot. Create one with @BotFather and copy its token.")
	token, err := ask("Bot token: ", true)
	if err != nil {
		return err
	}

	var me struct {
		Username string `json:"username"`
	}
	if err := telegram(token, "getMe", nil, &me); err != nil {
		return fmt.Errorf("Telegram does not accept this token: %v", err)
	}

	fmt.Printf("Now send any message to @%s in Telegram…\n", me.Username)
	var offset int64
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
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
			return err
		}
		for _, u := range updates {
			offset = u.ID + 1
			if u.Message == nil {
				continue
			}
			// Mark the message read, so den does not answer it as a request.
			telegram(token, "getUpdates", url.Values{"offset": {fmt.Sprint(offset)}, "timeout": {"0"}}, nil)

			chat := fmt.Sprint(u.Message.Chat.ID)
			if err := inside(c, chat, "secret", "set", "telegram_chat_id"); err != nil {
				return err
			}
			if err := inside(c, token, "secret", "set", "telegram_token"); err != nil {
				return err
			}
			fmt.Printf("Connected to your chat, %s.\n", u.Message.From.Name)
			return nil
		}
	}
	return errors.New("no message came in 5 minutes: run den setup again")
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
