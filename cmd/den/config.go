package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"strconv"
	"strings"
)

// den config shows and changes ~/.config/den/config.json, and restarts den
// so the change applies.

const configUsage = `Usage:
  den config                      Show the settings
  den config set <name> <value> [<name> <value>...]
                                  Change some and restart den
  den config unset <name>         Drop a variable of the environment and restart den

A name is memory (such as 2G), cpus, runtime (container, docker, or
podman), or a variable of den's environment in capitals, such as
DEN_WHISPER_MODEL.
`

// envName is the name of a variable of the environment, such as TZ.
var envName = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// memorySize is a size the container tools take, such as 2G or 1536M.
var memorySize = regexp.MustCompile(`^[0-9]+[KMGT]?$`)

func hostConfigCommand(c hostConfig, args []string) error {
	var err error
	switch {
	case len(args) == 0:
		data, err := json.MarshalIndent(c, "", "  ")
		if err != nil {
			return err
		}
		fmt.Printf("%s\n%s\n", configPath(), data)
		return nil
	case len(args) >= 3 && len(args)%2 == 1 && args[0] == "set":
		for i := 1; i < len(args) && err == nil; i += 2 {
			c, err = c.set(args[i], args[i+1])
		}
	case len(args) == 2 && args[0] == "unset":
		c, err = c.unset(args[1])
	default:
		fmt.Print(configUsage)
		return nil
	}
	if err != nil {
		return err
	}

	if err := c.save(); err != nil {
		return err
	}
	if !c.exists("inspect", c.Name) {
		done("Saved; den start applies it")
		return nil
	}
	return launch(c, "Restarting den once what runs ends", recreate)
}

// set gives the setting name the value.
func (c hostConfig) set(name, value string) (hostConfig, error) {
	value = strings.TrimSpace(value)
	switch {
	case name == "memory":
		if !memorySize.MatchString(value) {
			return c, fmt.Errorf("memory is a size such as 2G, not %q", value)
		}
		c.Memory = value
	case name == "cpus":
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return c, fmt.Errorf("cpus is a number of cores, not %q", value)
		}
		c.CPUs = n
	case name == "runtime":
		switch engine(value) {
		case appleContainer, docker, podman:
		default:
			return c, fmt.Errorf("runtime is container, docker, or podman, not %q", value)
		}
		c.Runtime = value
	case envName.MatchString(name):
		c.Env = maps.Clone(c.Env)
		if c.Env == nil {
			c.Env = map[string]string{}
		}
		c.Env[name] = value
	default:
		return c, fmt.Errorf("den has no setting %q\n\n%s", name, configUsage)
	}
	return c, nil
}

// unset drops the variable name from den's environment.
func (c hostConfig) unset(name string) (hostConfig, error) {
	if !envName.MatchString(name) {
		return c, fmt.Errorf("only a variable of the environment can be unset, such as DEN_WHISPER_MODEL; set %s instead", name)
	}
	if _, ok := c.Env[name]; !ok {
		return c, fmt.Errorf("den's environment has no %s", name)
	}
	c.Env = maps.Clone(c.Env)
	delete(c.Env, name)
	return c, nil
}
