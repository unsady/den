package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"charm.land/huh/v2"
	"charm.land/huh/v2/spinner"
)

// The host CLI's questions. In a terminal they take arrow keys, a filter,
// and masked input, and each answered one stays as a single line; when den
// reads a pipe, as in a script, they read plain lines.

var errStopped = errors.New("stopped")

func paint(code, text string) string {
	if !terminal() || os.Getenv("NO_COLOR") != "" {
		return text
	}
	return "\033[" + code + "m" + text + "\033[0m"
}

// done prints a finished step.
func done(format string, args ...any) {
	fmt.Printf("%s %s\n", paint("32", "✓"), fmt.Sprintf(format, args...))
}

// failed prints a step that did not work.
func failed(format string, args ...any) {
	fmt.Printf("%s %s\n", paint("31", "✗"), fmt.Sprintf(format, args...))
}

// answered leaves a question and its answer on one line.
func answered(title, answer string) {
	fmt.Printf("%s %s %s\n", paint("32", "?"), paint("1", title), paint("36", answer))
}

// note prints a line of help before a question.
func note(text string) {
	fmt.Println(paint("2", text))
}

func runField(field huh.Field) error {
	err := huh.NewForm(huh.NewGroup(field)).
		WithTheme(huh.ThemeFunc(huh.ThemeBase16)).
		WithShowHelp(false).
		Run()
	if errors.Is(err, huh.ErrUserAborted) {
		return errStopped
	}
	return err
}

// ask reads one answer, masked when secret. prompt may end with ": ".
func ask(prompt string, secret bool) (string, error) {
	title := strings.TrimSuffix(strings.TrimSpace(prompt), ":")
	if !terminal() {
		return askLine(title + ": ")
	}

	var value string
	field := huh.NewInput().Title(title).Value(&value)
	if secret {
		field.EchoMode(huh.EchoModePassword)
	}
	if err := runField(field); err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	shown := value
	if secret {
		shown = strings.Repeat("•", min(utf8.RuneCountInString(value), 16))
	}
	answered(title, shown)
	return value, nil
}

// input reads an answer that Enter leaves as fallback.
func input(title, fallback string) (string, error) {
	if !terminal() {
		value, err := askLine(fmt.Sprintf("%s [%s]: ", title, fallback))
		if value == "" {
			return fallback, nil
		}
		return value, err
	}

	var value string
	if err := runField(huh.NewInput().Title(title).Placeholder(fallback).Value(&value)); err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	answered(title, value)
	return value, nil
}

var stdinReader = bufio.NewReader(os.Stdin)

func askLine(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	line, err := stdinReader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" && err != nil {
		return "", fmt.Errorf("nothing entered for %q", strings.TrimSuffix(prompt, ": "))
	}
	return line, nil
}

// choice is one answer to choose: its label, a few words about it, and the
// value it gives.
type choice struct {
	label string
	about string
	value string
}

// choose asks for one of choices, starting at the one whose value is
// selected. Long lists filter as the user types.
func choose(title string, choices []choice, selected string) (string, error) {
	if len(choices) == 0 {
		return "", fmt.Errorf("%s: nothing to choose from", title)
	}
	if !terminal() {
		return chooseLine(title, choices, selected)
	}

	width := 0
	for _, c := range choices {
		width = max(width, utf8.RuneCountInString(c.label))
	}
	options := make([]huh.Option[string], len(choices))
	for i, c := range choices {
		key := c.label
		if c.about != "" {
			key = fmt.Sprintf("%-*s  %s", width, c.label, c.about)
		}
		options[i] = huh.NewOption(key, c.value)
	}

	value := selected
	long := len(choices) > 8
	field := huh.NewSelect[string]().Title(title).Options(options...).Value(&value).Filtering(long)
	if long {
		// The filter takes the title's line, so the title moves below it.
		field.Description(title + ": type to filter, ↑ ↓ to move, Enter to pick").Height(14)
	}
	if err := runField(field); err != nil {
		return "", err
	}
	for _, c := range choices {
		if c.value == value {
			answered(title, c.label)
		}
	}
	return value, nil
}

func chooseLine(title string, choices []choice, selected string) (string, error) {
	fmt.Println(title + ":")
	fallback := 1
	for i, c := range choices {
		fmt.Printf("  %d  %s  %s\n", i+1, c.label, c.about)
		if c.value == selected {
			fallback = i + 1
		}
	}
	for {
		answer, err := askLine(fmt.Sprintf("Number [%d]: ", fallback))
		if answer == "" {
			if err != nil {
				return "", err
			}
			return choices[fallback-1].value, nil
		}
		if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(choices) {
			return choices[n-1].value, nil
		}
		for _, c := range choices {
			if answer == c.value || answer == c.label {
				return c.value, nil
			}
		}
		fmt.Printf("Type a number from 1 to %d.\n", len(choices))
	}
}

// step runs work under a spinner titled with what it does.
func step(title string, work func(context.Context) error) error {
	if !terminal() {
		fmt.Println(title + "…")
		return work(context.Background())
	}
	return spinner.New().Title(" " + title + "…").ActionWithErr(work).Run()
}
