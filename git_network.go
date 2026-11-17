package main

import (
	"errors"
	"os/exec"
	"strings"
)

// Network commands accept only the documented option set and one explicit
// HTTPS URL or configuration-backed remote. Git's legacy files/groups and
// implicit destination heuristics must never select another authority.
func gitNetworkSyntax(args, options []string, command string) ([]string, []string, bool, error) {
	allowed := map[string]map[string]bool{
		"fetch":     {"--prune": true, "--tags": true, "--no-tags": true, "--quiet": true, "-q": true},
		"pull":      {"--ff-only": true, "--quiet": true, "-q": true},
		"push":      {"--set-upstream": true, "-u": true, "--quiet": true, "-q": true},
		"ls-remote": {"--heads": true, "--tags": true, "--refs": true, "--symref": true, "--quiet": true, "-q": true, "--exit-code": true},
	}
	flags, network := allowed[command]
	if !network {
		return nil, nil, false, nil
	}
	invalid := errors.New("network Git requires a documented option and an inspected named remote or configured HTTPS URL")
	var opts, positional []string
	for _, a := range args[len(options)+1:] {
		if strings.HasPrefix(a, "-") {
			if !flags[a] {
				return nil, nil, true, invalid
			}
			opts = append(opts, a)
		} else {
			if strings.ContainsAny(a, "\r\n\x00") {
				return nil, nil, true, invalid
			}
			positional = append(positional, a)
		}
	}
	if len(positional) == 0 && command != "pull" {
		return nil, nil, true, invalid
	}
	for i, ref := range positional {
		if i > 0 && strings.Contains(ref, "://") {
			return nil, nil, true, invalid
		}
	}
	return opts, positional, true, nil
}

func boundGitNetworkArgs(args, options []string, command string, env []string, origin string) ([]string, error) {
	opts, positional, network, err := gitNetworkSyntax(args, options, command)
	if err != nil {
		return nil, err
	}
	if !network {
		return args, nil
	}
	invalid := errors.New("network Git requires a documented option and an inspected named remote or configured HTTPS URL")
	probe := func(key string) ([]byte, error) {
		c := exec.Command("git", append(append([]string{}, options...), "config", "--get-all", key)...)
		c.Env = env
		return c.Output()
	}
	if len(positional) == 0 {
		if command != "pull" {
			return nil, invalid
		}
		c := exec.Command("git", append(append([]string{}, options...), "symbolic-ref", "--quiet", "--short", "HEAD")...)
		c.Env = env
		branch, err := c.Output()
		if err != nil {
			return nil, invalid
		}
		remote, err := probe("branch." + strings.TrimSpace(string(branch)) + ".remote")
		if err != nil || strings.TrimSpace(string(remote)) == "" {
			return nil, invalid
		}
		positional = append(positional, strings.TrimSpace(string(remote)))
	}
	remote := positional[0]
	if !validForgeClone(origin, remote) {
		urls, err := probe("remote." + remote + ".url")
		if err != nil || strings.TrimSpace(string(urls)) == "" {
			return nil, invalid
		}
		for _, raw := range strings.Split(strings.TrimSpace(string(urls)), "\n") {
			if !validForgeClone(origin, raw) {
				return nil, invalid
			}
		}
	}
	out := append(append([]string{}, options...), command)
	out = append(out, opts...)
	return append(out, positional...), nil
}
