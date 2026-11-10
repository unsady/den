package main

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/unsady/den/internal/codex"
)

// account is a way to pay for den's model that setup offers by name, so a
// new user picks what they have rather than a provider id and a model id.
type account struct {
	name  string
	about string
	// provider is the models.dev id, codex for a ChatGPT subscription, or
	// "" for one the user names.
	provider string
	// model is the one den starts with; /model in Telegram changes it.
	model string
	// keys is where the user creates an API key.
	keys string
}

var accounts = []account{
	{"OpenCode Go", "a monthly plan with many open models", "opencode-go", "deepseek-v4-pro", "https://opencode.ai/auth"},
	{"OpenRouter", "pay as you go, almost every model", "openrouter", "deepseek/deepseek-v4-pro", "https://openrouter.ai/settings/keys"},
	{"DeepSeek", "pay as you go, DeepSeek's own API", "deepseek", "deepseek-v4-pro", "https://platform.deepseek.com/api_keys"},
	{"ChatGPT Plus or Pro", "your subscription, signed in in the browser", "codex", "", ""},
	{"Another provider", "any provider on models.dev, by its id", "", "", ""},
}

// chooseAccount asks which account pays for den's model and connects it.
func chooseAccount(c hostConfig) error {
	fmt.Println("\nden needs a model to think with. Which account will it use?")
	for i, a := range accounts {
		fmt.Printf("  %d  %-20s %s\n", i+1, a.name, a.about)
	}
	a, err := pickAccount()
	if err != nil {
		return err
	}

	switch a.provider {
	case "codex":
		return connectHost(c, "codex", codex.Redirect, "ChatGPT")
	case "":
		provider, err := ask("Provider id, such as zai or moonshotai: ", false)
		if err != nil {
			return err
		}
		return login(c, provider, "", "")
	}

	model, err := chooseModel(a)
	if err != nil {
		return err
	}
	fmt.Println("Create an API key at " + a.keys)
	return login(c, a.provider, model, "")
}

func pickAccount() (account, error) {
	for {
		answer, err := ask("Number [1]: ", false)
		if err != nil {
			return account{}, err
		}
		if answer == "" {
			return accounts[0], nil
		}
		if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(accounts) {
			return accounts[n-1], nil
		}
		fmt.Printf("Type a number from 1 to %d.\n", len(accounts))
	}
}

// chooseModel offers the account's usual model, which Enter keeps, and
// finds another by a word.
func chooseModel(a account) (string, error) {
	models, err := catalogModels(a.provider)
	if err != nil || !slices.ContainsFunc(models, func(m catalogModel) bool { return m.ID == a.model }) {
		return pickModel(a.provider)
	}

	fmt.Println("/model in Telegram changes the model at any time.")
	answer, err := ask(fmt.Sprintf("Model [%s], Enter to keep it or a word to find another: ", a.model), false)
	if err != nil {
		return "", err
	}
	if answer == "" {
		return a.model, nil
	}
	return findModel(models, answer)
}
