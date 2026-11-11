// Package seed creates the agent's space from the files embedded in den and
// links den's built-in skills where fx finds them.
package seed

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/unsady/den/internal/config"
	"github.com/unsady/den/internal/fetch"
	"github.com/unsady/den/internal/proc"
)

//go:embed all:space
var files embed.FS

// rules are den's instructions for every session. fx reads them from its
// profile, ~/.fx/AGENTS.md, beside the space's own AGENTS.md, which is the
// agent's to change.
//
//go:embed fx/AGENTS.md
var rules []byte

// Init creates the space and the archive when they are missing, and links
// the built-in skills. It never overwrites a file of
// the agent's, so it is safe to run on every start.
func Init(cfg config.Config) error {
	if err := os.MkdirAll(cfg.Archive, 0o755); err != nil {
		return err
	}

	if _, err := os.Stat(filepath.Join(cfg.Space, ".git")); err == nil {
		if err := removeShipped(cfg.Space); err != nil {
			fmt.Printf("could not remove den's old skill copies: %v\n", err)
		}
	} else if err := create(cfg.Space); err != nil {
		return err
	}

	return linkSkills(cfg)
}

// WriteRules writes den's rules to fx's profile, with the names of the
// APIs the user connected, when they differ from what is there. An
// unchanged file stays untouched, so fx does not take it for new
// instructions in the middle of a session. The daemon writes them, since
// only it reads the user's services.
func WriteRules(cfg config.Config) error {
	if cfg.Sessions == "" {
		return nil
	}
	services, err := fetch.LoadCustom(cfg)
	if err != nil {
		return err
	}
	text := rules
	if len(services) > 0 {
		names := make([]string, len(services))
		for i, s := range services {
			names[i] = s.Name
		}
		sort.Strings(names)
		text = append(slices.Clone(rules), fmt.Sprintf("- The user connected these APIs, which you call through den with tokens it holds: %s. `den service ls` says what each one does.\n", strings.Join(names, ", "))...)
	}

	// The sessions live at ~/.fx/sessions/v2, the profile above them.
	profile := filepath.Dir(filepath.Dir(cfg.Sessions))
	path := filepath.Join(profile, "AGENTS.md")
	// The profile is the agent's, so den writes there as the agent: a link
	// the agent put in place of the file leads nowhere else.
	if have, err := proc.ReadFile(cfg, path); err == nil && bytes.Equal(have, text) {
		return nil
	}
	return proc.WriteFile(cfg, path, text, 0o644)
}

func create(space string) error {
	err := fs.WalkDir(files, "space", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("space", path)
		dest := filepath.Join(space, rel)

		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		if _, err := os.Stat(dest); err == nil {
			return nil
		}
		data, err := files.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dest, data, 0o644)
	})
	if err != nil {
		return err
	}

	if err := commit(space); err != nil {
		return fmt.Errorf("git: %w", err)
	}
	fmt.Printf("created the space at %s\n", space)
	return nil
}

// linkSkills links each built-in skill into fx's profile skills, where the
// agent sees it but cannot change it, and removes links to built-in skills
// that are gone. A file or folder of the agent's own under the same name is
// left alone.
func linkSkills(cfg config.Config) error {
	if cfg.Skills == "" {
		return nil
	}
	entries, err := os.ReadDir(cfg.Skills)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.FXSkills, 0o755); err != nil {
		return err
	}

	builtin := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		builtin[e.Name()] = true
		target := filepath.Join(cfg.Skills, e.Name())
		link := filepath.Join(cfg.FXSkills, e.Name())

		if have, err := os.Readlink(link); err == nil {
			if have == target {
				continue
			}
			if err := os.Remove(link); err != nil {
				return err
			}
		} else if _, err := os.Lstat(link); err == nil {
			fmt.Printf("not linking the built-in skill %s: %s already exists\n", e.Name(), link)
			continue
		}
		if err := os.Symlink(target, link); err != nil {
			return err
		}
	}

	links, err := os.ReadDir(cfg.FXSkills)
	if err != nil {
		return err
	}
	for _, e := range links {
		link := filepath.Join(cfg.FXSkills, e.Name())
		have, err := os.Readlink(link)
		if err != nil || builtin[e.Name()] || !strings.HasPrefix(have, cfg.Skills+string(filepath.Separator)) {
			continue
		}
		if err := os.Remove(link); err != nil {
			return err
		}
	}
	return nil
}

// shipped holds the SHA-256 of every version of the skills that den used
// to copy into the space. Those copies are removed now that the skills are
// built in, unless the agent changed them.
var shipped = map[string][]string{
	"den": {
		"9ae78e5599d86099984b5ee4956bd1583fcc739f140aba475b3eee82f63e904b",
		"cc4d21958a6ed64251567ecef8b97fbe35789955b7f1d890282e4cdc60dd55cf",
	},
	"reflect": {
		"03df40be297b494563e5d68c7589cbff585607f871ad14ffe404002599ed92d8",
	},
}

func removeShipped(space string) error {
	var removed []string
	for name, sums := range shipped {
		dir := filepath.Join(".fx", "skills", name)
		entries, err := os.ReadDir(filepath.Join(space, dir))
		if err != nil || len(entries) != 1 || entries[0].Name() != "SKILL.md" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(space, dir, "SKILL.md"))
		if err != nil {
			continue
		}
		sum := sha256.Sum256(data)
		for _, s := range sums {
			if s == hex.EncodeToString(sum[:]) {
				removed = append(removed, dir)
			}
		}
	}
	if len(removed) == 0 {
		return nil
	}

	for _, dir := range removed {
		if err := os.RemoveAll(filepath.Join(space, dir)); err != nil {
			return err
		}
	}
	// Only these paths go into the commit, so the agent's uncommitted work
	// stays as it was.
	args := append([]string{"commit", "-q", "-m", "Remove den's skill copies, which are now built in", "--"}, removed...)
	cmd := exec.Command("git", args...)
	cmd.Dir = space
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %v: %s", err, out)
	}
	fmt.Printf("removed den's skill copies from the space: %s\n", strings.Join(removed, ", "))
	return nil
}

// commit makes the space a repository with its first commit, so every later
// change the agent makes is a diff.
func commit(dir string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return errors.New("git is not installed; the space is not versioned")
	}

	steps := [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", "den"},
		{"config", "user.email", "den@localhost"},
		{"add", "-A"},
		{"commit", "-q", "-m", "Create the space"},
	}
	for _, args := range steps {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %v: %s", args[0], err, out)
		}
	}
	return nil
}
