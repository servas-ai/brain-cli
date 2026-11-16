package main

import (
	"net/url"
	"os/exec"
	"strings"
)

// Only the established HTTPS GitHub route may use its existing credentials.
// Every other network destination needs the isolated Human OIDC worker, even
// without a Forge marker or with an absent, corrupt or mismatched profile.
func githubHTTPS(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == "github.com" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && strings.HasPrefix(u.Path, "/") && !strings.ContainsAny(raw, "\r\n\x00\\")
}

func brainGitNeedsOIDC(args []string) bool {
	var options []string
	i := 0
	for i+1 < len(args) && args[i] == "-C" {
		options = append(options, args[i], args[i+1])
		i += 2
	}
	if i >= len(args) {
		return true
	}
	switch args[i] {
	case "clone":
		var raw string
		for _, a := range args[i+1:] {
			if !strings.HasPrefix(a, "-") {
				raw = a
				break
			}
		}
		if !githubHTTPS(raw) {
			return true
		}
		// Git applies insteadOf before contacting the remote. Inspect that
		// effective destination without performing a network request.
		out, err := exec.Command("git", "ls-remote", "--get-url", raw).Output()
		return err != nil || !githubHTTPS(strings.TrimSpace(string(out)))
	case "pull":
		out, err := exec.Command("git", append(options, "remote")...).Output()
		if err != nil || strings.TrimSpace(string(out)) == "" {
			return true
		}
		for _, remote := range strings.Fields(string(out)) {
			for _, push := range []bool{false, true} {
				probe := append(append([]string{}, options...), "remote", "get-url", "--all")
				if push {
					probe = append(probe, "--push")
				}
				out, err := exec.Command("git", append(probe, remote)...).Output()
				if err != nil || strings.TrimSpace(string(out)) == "" {
					return true
				}
				for _, raw := range strings.Split(strings.TrimSpace(string(out)), "\n") {
					if !githubHTTPS(raw) {
						return true
					}
				}
			}
		}
	}
	return false
}

func preflightBrainGit(args []string) error {
	if !forgeGitMode && !brainGitNeedsOIDC(args) {
		return nil
	}
	c := authenticatedGit(args)
	defer c.cleanup()
	return c.preparationError
}
