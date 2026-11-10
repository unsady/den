package main

import (
	"strconv"

	"github.com/unsady/den/internal/codex"
)

// account is a way to pay for den's model that setup offers by name, so a
// new user picks what they have rather than a provider and a model id.
type account struct {
	name  string
	about string
	// provider is the models.dev id, codex for a ChatGPT subscription, or
	// "" for one the user picks from models.dev.
	provider string
	// model is the one den suggests; /model in Telegram changes it.
	model string
	// keys is where the user creates an API key.
	keys string
}

var accounts = []account{
	{"OpenCode Go", "a monthly plan with many open models", "opencode-go", "deepseek-v4-pro", "https://opencode.ai/auth"},
	{"OpenRouter", "pay as you go, almost every model", "openrouter", "deepseek/deepseek-v4-pro", "https://openrouter.ai/settings/keys"},
	{"DeepSeek", "pay as you go, DeepSeek's own API", "deepseek", "deepseek-v4-pro", "https://platform.deepseek.com/api_keys"},
	{"ChatGPT Plus or Pro", "your subscription, signed in in the browser", "codex", "", ""},
	{"Another provider", "any of models.dev's", "", "", ""},
}

// chooseAccount asks which account pays for den's model and connects it.
func chooseAccount(c hostConfig) error {
	choices := make([]choice, len(accounts))
	for i, a := range accounts {
		choices[i] = choice{label: a.name, about: a.about, value: strconv.Itoa(i)}
	}
	answer, err := choose("Which account pays for den's model?", choices, "0")
	if err != nil {
		return err
	}
	n, _ := strconv.Atoi(answer)
	a := accounts[n]

	switch a.provider {
	case "codex":
		if err := connectHost(c, "codex", codex.Redirect, "ChatGPT"); err != nil {
			return err
		}
		done("ChatGPT connected; /model in Telegram picks its model")
		return nil
	case "":
		cat, err := catalog()
		if err != nil {
			return err
		}
		provider, err := choose("Provider", providerChoices(cat), "")
		if err != nil {
			return err
		}
		return login(c, provider, "", "")
	}
	return login(c, a.provider, "", "")
}
