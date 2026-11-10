package main

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"
)

type catalogProvider struct {
	Name string `json:"name"`
	// API is the provider's OpenAI-compatible address. den's model proxy
	// works only with such providers.
	API    string                  `json:"api"`
	Models map[string]catalogModel `json:"models"`
}

type catalogModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Cost *price `json:"cost"`
}

// price is what a model costs per million tokens.
type price struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
}

func (m catalogModel) free() bool {
	return m.Cost != nil && m.Cost.Input == 0 && m.Cost.Output == 0
}

var loadedCatalog map[string]catalogProvider

// catalog reads models.dev's providers and models once, under a spinner.
func catalog() (map[string]catalogProvider, error) {
	if loadedCatalog != nil {
		return loadedCatalog, nil
	}
	err := step("Loading models from models.dev", func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, "GET", "https://models.dev/api.json", nil)
		if err != nil {
			return err
		}
		resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		return json.NewDecoder(resp.Body).Decode(&loadedCatalog)
	})
	return loadedCatalog, err
}

// pickModel asks for one of a provider's models, starting at the one its
// account suggests.
func pickModel(provider string) (string, error) {
	cat, err := catalog()
	if err != nil || len(cat[provider].Models) == 0 {
		return ask("Model id", false)
	}

	suggested := ""
	for _, a := range accounts {
		if a.provider == provider {
			suggested = a.model
		}
	}
	return choose("Model", modelChoices(cat[provider].Models, suggested), suggested)
}

// modelChoices lists models by id, with the suggested one first.
func modelChoices(models map[string]catalogModel, suggested string) []choice {
	var choices []choice
	for id, m := range models {
		if m.ID == "" {
			m.ID = id
		}
		about := m.Name
		if m.free() {
			about += " · free"
		}
		choices = append(choices, choice{label: m.ID, about: about, value: m.ID})
	}
	slices.SortFunc(choices, func(a, b choice) int {
		switch {
		case a.value == suggested:
			return -1
		case b.value == suggested:
			return 1
		}
		return strings.Compare(a.value, b.value)
	})
	return choices
}

// providerChoices lists the providers den can use, by name.
func providerChoices(cat map[string]catalogProvider) []choice {
	var choices []choice
	for id, p := range cat {
		if p.API != "" && len(p.Models) > 0 {
			choices = append(choices, choice{label: p.Name, about: id, value: id})
		}
	}
	slices.SortFunc(choices, func(a, b choice) int { return strings.Compare(strings.ToLower(a.label), strings.ToLower(b.label)) })
	return choices
}
