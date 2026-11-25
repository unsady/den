package main

import (
	"strings"
	"testing"
)

func TestConfigSet(t *testing.T) {
	c := hostConfig{CPUs: 2, Memory: "1G", Env: map[string]string{"TZ": "UTC"}}

	got, err := c.set("memory", "2G")
	if err != nil || got.Memory != "2G" {
		t.Fatalf("memory: %+v, %v", got, err)
	}
	got, err = got.set("cpus", "4")
	if err != nil || got.CPUs != 4 {
		t.Fatalf("cpus: %+v, %v", got, err)
	}
	got, err = got.set("DEN_WHISPER_MODEL", "small-q8_0")
	if err != nil || got.Env["DEN_WHISPER_MODEL"] != "small-q8_0" || got.Env["TZ"] != "UTC" {
		t.Fatalf("env: %+v, %v", got, err)
	}
	// The config it came from keeps its own environment.
	if _, ok := c.Env["DEN_WHISPER_MODEL"]; ok {
		t.Fatal("set changed the original's environment")
	}
	got, err = got.unset("DEN_WHISPER_MODEL")
	if err != nil || len(got.Env) != 1 {
		t.Fatalf("unset: %+v, %v", got, err)
	}

	for _, bad := range [][2]string{
		{"memory", "lots"}, {"cpus", "0"}, {"runtime", "lxc"}, {"home", "/tmp"}, {"den_jobs", "2"},
	} {
		if _, err := c.set(bad[0], bad[1]); err == nil {
			t.Errorf("set %s %s took it", bad[0], bad[1])
		}
	}
	if _, err := c.unset("memory"); err == nil || !strings.Contains(err.Error(), "only a variable") {
		t.Errorf("unset memory: %v", err)
	}
	if _, err := c.unset("NOPE"); err == nil {
		t.Error("unset of a missing variable took it")
	}
	if got, err := (hostConfig{}).set("TZ", "UTC"); err != nil || got.Env["TZ"] != "UTC" {
		t.Errorf("set on no environment: %+v, %v", got, err)
	}
}
