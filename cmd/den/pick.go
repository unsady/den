package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// shownModels is the most models one search lists.
const shownModels = 15

type catalogModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Cost *struct {
		Input  float64 `json:"input"`
		Output float64 `json:"output"`
	} `json:"cost"`
}

func (m catalogModel) free() bool {
	return m.Cost != nil && m.Cost.Input == 0 && m.Cost.Output == 0
}

// pickModel lets the user find a provider's model by a part of its name,
// from models.dev, so they need not know its id.
func pickModel(provider string) (string, error) {
	models, err := catalogModels(provider)
	if err != nil || len(models) == 0 {
		return ask("Model id: ", false)
	}

	fmt.Printf("\n%s has %d models. Type part of a name, such as deep, glm, or claude, or Enter for all.\n", provider, len(models))
	query, err := ask("> ", false)
	if err != nil {
		return "", err
	}
	return findModel(models, query)
}

// findModel narrows models by query until the user picks one.
func findModel(models []catalogModel, query string) (string, error) {
	found := models
	for {
		// More words narrow the last list; words it lacks search them all.
		if narrowed := matchModels(found, query); len(narrowed) > 0 {
			found = narrowed
		} else {
			found = matchModels(models, query)
		}
		switch {
		case len(found) == 0:
			fmt.Println("Nothing matches. Try another word.")
			found = models
		case len(found) == 1:
			fmt.Println("Model:", found[0].ID)
			return found[0].ID, nil
		default:
			for i, m := range found[:min(len(found), shownModels)] {
				line := fmt.Sprintf("  %2d  %-28s %s", i+1, m.ID, m.Name)
				if m.free() {
					line += "  · free"
				}
				fmt.Println(line)
			}
			if len(found) > shownModels {
				fmt.Printf("  … and %d more: type more of the name\n", len(found)-shownModels)
			}
		}

		var err error
		query, err = ask("Number, or other words: ", false)
		if err != nil {
			return "", err
		}
		if n, convErr := strconv.Atoi(query); convErr == nil && n >= 1 && n <= min(len(found), shownModels) {
			fmt.Println("Model:", found[n-1].ID)
			return found[n-1].ID, nil
		}
	}
}

// matchModels finds the models whose id or name holds every word of query,
// or the exact id alone.
func matchModels(models []catalogModel, query string) []catalogModel {
	words := strings.Fields(strings.ToLower(query))
	var found []catalogModel
	for _, m := range models {
		if m.ID == query {
			return []catalogModel{m}
		}
		text := strings.ToLower(m.ID + " " + m.Name)
		if !slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(text, w) }) {
			found = append(found, m)
		}
	}
	return found
}

func catalogModels(provider string) ([]catalogModel, error) {
	client := &http.Client{Timeout: time.Minute}
	resp, err := client.Get("https://models.dev/api.json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var catalog map[string]struct {
		Models map[string]catalogModel `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&catalog); err != nil {
		return nil, err
	}
	var models []catalogModel
	for id, m := range catalog[provider].Models {
		if m.ID == "" {
			m.ID = id
		}
		models = append(models, m)
	}
	slices.SortFunc(models, func(a, b catalogModel) int { return strings.Compare(a.ID, b.ID) })
	return models, nil
}
