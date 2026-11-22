package main

import (
	"errors"

	"github.com/unsady/den/internal/fetch"
)

// hostService runs den service in the container. add without a name and
// an address asks for each, so the user needs no flags to add an API.
func hostService(c hostConfig, args []string) error {
	if len(args) < 2 || args[1] != "add" {
		return inside(c, "", args...)
	}
	if len(args) > 2 {
		token, err := ask("Token", true)
		if err != nil {
			return err
		}
		return inside(c, token, args...)
	}

	note("den calls the API with your token and keeps the token from the agent.")
	note("Not sure what to enter? Ask the agent in Telegram, such as: help me connect Todoist.")
	s, err := askService()
	if err != nil {
		return err
	}
	token, err := ask("Token", true)
	if err != nil {
		return err
	}
	add := []string{"service", "add", "--ask", s.Ask}
	if s.Header != "" {
		add = append(add, "--header", s.Header)
	}
	if s.About != "" {
		add = append(add, "--about", s.About)
	}
	return inside(c, token, append(add, s.Name, "https://"+s.Host)...)
}

// askService asks for a service's name, address, header, and which
// requests wait for the user, and checks each answer as it comes.
func askService() (fetch.Custom, error) {
	name, err := askValid("Name, such as crm", func(v string) error {
		_, err := fetch.NewCustom(v, "https://example.com", "", "", "")
		return err
	})
	if err != nil {
		return fetch.Custom{}, err
	}
	address, err := askValid("API address, such as https://api.example.com", func(v string) error {
		_, err := fetch.NewCustom(name, v, "", "", "")
		return err
	})
	if err != nil {
		return fetch.Custom{}, err
	}

	const bearer = "Authorization: Bearer"
	header, err := input("Header for the token", bearer)
	if err != nil {
		return fetch.Custom{}, err
	}
	if header == bearer {
		header = ""
	}

	mode, err := choose("Which requests wait for your Allow in Telegram?", []choice{
		{"Writes", "reads go at once", fetch.AskWrites},
		{"Deletes", "reads and other writes go at once", fetch.AskDeletes},
		{"Every request", "", fetch.AskAlways},
		{"None", "the agent writes on its own", fetch.AskNever},
	}, fetch.AskWrites)
	if err != nil {
		return fetch.Custom{}, err
	}

	note("The agent reads this to know how to use the API.")
	about, err := ask("What the API does and where its docs are", false)
	if err != nil {
		return fetch.Custom{}, err
	}
	return fetch.NewCustom(name, address, header, mode, about)
}

// askValid asks until check accepts the answer, or once without a terminal.
func askValid(title string, check func(string) error) (string, error) {
	for {
		value, err := ask(title, false)
		if err != nil {
			return "", err
		}
		err = check(value)
		if err == nil {
			return value, nil
		}
		if !terminal() {
			return "", err
		}
		failed("%v", err)
	}
}

// connectChoices are what den connect offers when it is run alone.
var connectChoices = []choice{
	{"mail", "any mailbox, with an app password", "mail"},
	{"google", "Gmail, Calendar, and Drive, with your own Google client", "google"},
	{"codex", "a ChatGPT Plus or Pro subscription", "codex"},
	{"grok", "a SuperGrok subscription", "grok"},
	{"claude", "a Claude Pro or Max subscription", "claude"},
}

func chooseConnect() (string, error) {
	if !terminal() {
		return "", errors.New("connect google, codex, grok, claude, or mail")
	}
	return choose("What to connect?", connectChoices, "mail")
}
