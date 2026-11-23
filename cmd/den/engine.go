package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/unsady/den/internal/app"
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
		args = append(args, "--cpus", fmt.Sprint(c.CPUs), "-m", c.Memory, "--restart", "unless-stopped",
			// Nothing the agent runs gains more rights than it started with,
			// such as through a setuid program.
			"--security-opt", "no-new-privileges",
			// A command that forks without end fills the container, not the
			// machine or the VM other containers share.
			"--pids-limit", "512")
	}
	args = append(args, "-v", c.Home+":/home/agent", "-v", c.Volume+":/var/lib/den")
	if c.Backups != "" {
		args = append(args, "-v", c.Backups+":"+backupsMount, "-e", "DEN_BACKUPS="+backupsMount)
	}
	if c.App != "" {
		port := fmt.Sprint(app.Port)
		args = append(args, "-p", net.JoinHostPort(c.App, port)+":"+port)
	}
	for k, v := range c.Env {
		args = append(args, "-e", k+"="+v)
	}
	return append(args, c.Image)
}

// backupsMount is where the container sees the host's backups folder.
const backupsMount = "/var/backups/den"

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
