package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// On the host, den runs its container with one of three tools: Apple's
// container on a Mac, or Docker or Podman, which take the same commands
// as each other.

type engine string

const (
	appleContainer engine = "container"
	docker         engine = "docker"
	podman         engine = "podman"
)

// pickEngine returns the tool the settings name, or the first one this
// machine has: Apple's container on a Mac, then Docker, then Podman.
func pickEngine(saved string) engine {
	if saved != "" {
		return engine(saved)
	}

	candidates := []engine{docker, podman}
	if runtime.GOOS == "darwin" {
		candidates = append([]engine{appleContainer}, candidates...)
	}
	for _, e := range candidates {
		if _, err := exec.LookPath(string(e)); err == nil {
			return e
		}
	}
	return candidates[0]
}

func (e engine) command(args ...string) *exec.Cmd {
	return exec.Command(string(e), args...)
}

// runArgs are the arguments that start den's container. Docker and Podman
// restart it after the machine does; Apple's container has no such policy,
// so on a Mac den start brings it back.
func (e engine) runArgs(c hostConfig) []string {
	args := []string{"run", "-d", "--name", c.Name}
	if e == appleContainer {
		args = append(args, "-c", fmt.Sprint(c.CPUs), "-m", c.Memory)
	} else {
		args = append(args, "--cpus", fmt.Sprint(c.CPUs), "-m", c.Memory, "--restart", "unless-stopped")
	}
	args = append(args, "-v", c.Home+":/home/agent", "-v", c.Volume+":/var/lib/den")
	for k, v := range c.Env {
		args = append(args, "-e", k+"="+v)
	}
	return append(args, c.Image)
}

// inContainer reports whether den runs in its own image, where the
// commands go to the den daemon rather than to a container.
func inContainer() bool {
	_, err := os.Stat("/run/den")
	return err == nil
}

// openBrowser opens link in the user's browser, if this machine can.
func openBrowser(link string) {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	exec.Command(name, link).Run()
}

// removeImage removes the image tag. The tool frees the image's space
// only when no other tag names it, and Docker and Podman keep an image a
// container still uses.
func (e engine) removeImage(image string) *exec.Cmd {
	if e == appleContainer {
		return e.command("image", "delete", image)
	}
	return e.command("image", "rm", image)
}

// previous is the tag that holds the image an update replaces, until the
// new one runs: den:latest becomes den:previous. An image pinned by
// digest has none.
func previous(image string) string {
	if strings.Contains(image, "@") {
		return ""
	}

	repo := image
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		repo = image[:i]
	}
	if repo+":previous" == image {
		return ""
	}
	return repo + ":previous"
}
