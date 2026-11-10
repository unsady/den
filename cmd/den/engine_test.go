package main

import (
	"slices"
	"strings"
	"testing"
)

func TestRunArgs(t *testing.T) {
	c := hostConfig{Name: "den", Image: "den:latest", Volume: "den", Home: "/home/me/den", CPUs: 2, Memory: "1G", Env: map[string]string{"TZ": "America/New_York"}}

	apple := strings.Join(appleContainer.runArgs(c), " ")
	if want := "run -d --name den -c 2 -m 1G -v /home/me/den:/home/agent -v den:/var/lib/den -e TZ=America/New_York den:latest"; apple != want {
		t.Errorf("container:\n got %s\nwant %s", apple, want)
	}

	for _, e := range []engine{docker, podman} {
		args := e.runArgs(c)
		got := strings.Join(args, " ")
		if want := "run -d --name den --cpus 2 -m 1G --restart unless-stopped -v /home/me/den:/home/agent -v den:/var/lib/den -e TZ=America/New_York den:latest"; got != want {
			t.Errorf("%s:\n got %s\nwant %s", e, got, want)
		}
		if slices.Contains(args, "-c") {
			t.Errorf("%s takes --cpus, not -c", e)
		}
	}
}

func TestPickEngine(t *testing.T) {
	if got := pickEngine("podman"); got != podman {
		t.Errorf("a saved runtime wins, got %s", got)
	}
}
