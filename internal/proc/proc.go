// Package proc starts programs on the agent's behalf.
//
// When den runs as root, the programs run as the agent user, so they cannot
// read den's secrets. Otherwise they run as the current user.
package proc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/unsady/den/internal/config"
)

// Command builds cmd for name and args. Cancelling ctx kills its whole
// process group, including the shells fx starts.
func Command(ctx context.Context, cfg config.Config, name string, args ...string) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 10 * time.Second

	if cfg.AgentUser == "" || os.Geteuid() != 0 {
		return cmd, nil
	}

	u, err := user.Lookup(cfg.AgentUser)
	if err != nil {
		return nil, fmt.Errorf("agent user: %w", err)
	}
	uid, _ := strconv.ParseUint(u.Uid, 10, 32)
	gid, _ := strconv.ParseUint(u.Gid, 10, 32)

	cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
	cmd.Env = setEnv(cmd.Env, "HOME", u.HomeDir)
	cmd.Env = setEnv(cmd.Env, "USER", u.Username)
	cmd.Env = setEnv(cmd.Env, "LOGNAME", u.Username)
	// The agent installs its own tools, such as yt-dlp, in ~/.local/bin.
	// Only its processes look there: den itself runs as root and must never
	// run a program the agent can replace.
	cmd.Env = setEnv(cmd.Env, "PATH", u.HomeDir+"/.local/bin:"+os.Getenv("PATH"))
	return cmd, nil
}

// OwnHome gives the agent user its home directory when den runs as root. A
// volume mounted over the home starts out owned by root, which would leave
// the agent unable to write there. Only the directory itself changes hands;
// what the agent created keeps its owner.
func OwnHome(cfg config.Config) error {
	if cfg.AgentUser == "" || os.Geteuid() != 0 {
		return nil
	}

	u, err := user.Lookup(cfg.AgentUser)
	if err != nil {
		return err
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)

	if err := os.MkdirAll(u.HomeDir, 0o755); err != nil {
		return err
	}
	err = os.Chown(u.HomeDir, uid, gid)
	if errors.Is(err, os.ErrPermission) {
		// A host folder shared into a VM, such as virtiofs under Apple's
		// container, refuses chown but lets every user in the guest write.
		return nil
	}
	return err
}

// OOMKills counts the processes the kernel has killed for lack of memory:
// in den's own cgroup when it has one, else on the whole machine, which in
// a VM-backed container is the container.
func OOMKills() int64 {
	for _, path := range []string{"/sys/fs/cgroup/memory.events", "/proc/vmstat"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if n, ok := strings.CutPrefix(line, "oom_kill "); ok {
				count, _ := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
				return count
			}
		}
	}
	return 0
}

// OutOfMemory reports whether err is a process killed by the kernel for
// lack of memory, given OOMKills from before the process started.
func OutOfMemory(err error, before int64) bool {
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return false
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	return ok && status.Signaled() && status.Signal() == syscall.SIGKILL && OOMKills() > before
}

// MemoryLimit describes how much memory den's container has, such as "1.0 GB".
func MemoryLimit() string {
	if data, err := os.ReadFile("/sys/fs/cgroup/memory.max"); err == nil {
		if n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64); err == nil {
			return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
		}
	}
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if kb, ok := strings.CutPrefix(line, "MemTotal:"); ok {
				n, _ := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(kb), "kB")), 10, 64)
				return fmt.Sprintf("%.1f GB", float64(n)/(1<<20))
			}
		}
	}
	return "an unknown amount"
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}
