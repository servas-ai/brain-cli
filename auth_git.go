package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Git must not consult ambient PATs, cookies, .netrc, client certificates or
// URL-specific TLS/auth settings. Configuration isolation complements, rather
// than replaces, the server's OIDC identity and repository ACL checks.
func cleanGitEnv() []string {
	var out []string
	for _, s := range os.Environ() {
		k, _, _ := strings.Cut(s, "=")
		keepGit := k == "GIT_SSL_CAINFO" || k == "GIT_SSL_CAPATH" || k == "GIT_AUTHOR_NAME" || k == "GIT_AUTHOR_EMAIL" || k == "GIT_AUTHOR_DATE" || k == "GIT_COMMITTER_NAME" || k == "GIT_COMMITTER_EMAIL" || k == "GIT_COMMITTER_DATE"
		if (strings.HasPrefix(k, "GIT_") && !keepGit) || k == "SSH_ASKPASS" || k == "SSH_AUTH_SOCK" || k == "CURL_HOME" || k == "NETRC" || k == "_NETRC" || k == "SSLKEYLOGFILE" || k == "_BRAIN_GIT_AUTH_DIR" {
			continue
		}
		out = append(out, s)
	}
	return append(out, "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_TRACE_REDACT=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_ALLOW_PROTOCOL=https", "GIT_PAGER=cat")
}

func gitEnvSet(env []string, key, value string) []string {
	var out []string
	for _, s := range env {
		if !strings.HasPrefix(s, key+"=") {
			out = append(out, s)
		}
	}
	return append(out, key+"="+value)
}

// The wrapper owns its empty process home and removes it after Git exits.
type authGitCommand struct {
	*exec.Cmd
	preparationError error
	cleanup          func()
}

func (c *authGitCommand) Run() error {
	defer c.cleanup()
	if c.preparationError != nil {
		return c.preparationError
	}
	return c.Cmd.Run()
}
func (c *authGitCommand) CombinedOutput() ([]byte, error) {
	defer c.cleanup()
	if c.preparationError != nil {
		return nil, c.preparationError
	}
	return c.Cmd.CombinedOutput()
}

func gitArguments(args []string, origin string) ([]string, string, error) {
	var options []string
	i := 0
	invalid := errors.New("unsupported or unsafe Git arguments; use the documented Brain workflow")
	for i < len(args) {
		a := args[i]
		if a == "-C" {
			if i+1 >= len(args) {
				return nil, "", invalid
			}
			options = append(options, a, args[i+1])
			i += 2
			continue
		}
		if a == "-c" {
			if i+1 >= len(args) {
				return nil, "", invalid
			}
			key, _, ok := strings.Cut(args[i+1], "=")
			if !ok || (key != "user.name" && key != "user.email") {
				return nil, "", invalid
			}
			options = append(options, a, args[i+1])
			i += 2
			continue
		}
		if a == "--no-pager" {
			options = append(options, a)
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			return nil, "", invalid
		}
		break
	}
	if i == len(args) {
		return nil, "", invalid
	}
	command := args[i]
	allowed := map[string]bool{"clone": true, "fetch": true, "push": true, "pull": true, "ls-remote": true, "switch": true, "add": true, "commit": true, "config": true, "status": true, "diff": true, "show": true, "log": true, "branch": true, "checkout": true, "sparse-checkout": true, "rev-parse": true}
	if !allowed[command] {
		return nil, "", invalid
	}
	if command == "clone" {
		if err := gitCloneArguments(args[i+1:], origin); err != nil {
			return nil, "", err
		}
	}
	if command == "config" {
		fields := args[i+1:]
		if len(fields) > 0 && (fields[0] == "--local" || fields[0] == "--get") {
			fields = fields[1:]
		}
		if len(fields) < 1 || len(fields) > 2 || (fields[0] != "user.name" && fields[0] != "user.email") {
			return nil, "", invalid
		}
	}
	for _, a := range args[i+1:] {
		if strings.HasPrefix(a, "--rec") {
			return nil, "", invalid
		}
		if (a == "-c" && command == "clone") || strings.HasPrefix(a, "--config") || a == "--global" || a == "--system" || a == "--file" || a == "-f" && command == "config" {
			return nil, "", invalid
		}
		if strings.Contains(a, "://") && !validForgeClone(origin, a) {
			return nil, "", errors.New("Git URL is outside the configured HTTPS Forge origin")
		}
	}
	return options, command, nil
}

// Clone creates its repository after preflight. Accept only the small options
// needed by this workflow; templates/config/reference/submodule knobs can load
// a different transport/auth configuration before its first network request.
func gitCloneArguments(args []string, origin string) error {
	invalid := errors.New("unsupported clone option; use HTTPS clone without templates or transport overrides")
	positional := 0
	endOptions := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" && !endOptions {
			endOptions = true
			continue
		}
		if !endOptions && strings.HasPrefix(a, "-") {
			if a == "--no-checkout" || a == "--single-branch" || a == "--quiet" || a == "-q" {
				continue
			}
			key, value, hasValue := strings.Cut(a, "=")
			if key != "--depth" && key != "--branch" && key != "--filter" {
				return invalid
			}
			if !hasValue {
				i++
				if i >= len(args) {
					return invalid
				}
				value = args[i]
			}
			if value == "" || strings.HasPrefix(value, "-") || strings.ContainsAny(value, "\r\n\x00") {
				return invalid
			}
			if key == "--depth" {
				n, err := strconv.Atoi(value)
				if err != nil || n < 1 {
					return invalid
				}
			}
			if key == "--filter" && value != "blob:none" {
				return invalid
			}
			continue
		}
		if positional == 0 && !validForgeClone(origin, a) {
			return errors.New("clone requires a repository on the configured HTTPS Forge origin")
		}
		positional++
	}
	if positional < 1 || positional > 2 {
		return invalid
	}
	return nil
}

func checkGitLocalConfig(options, env []string, origin string) error {
	probe := exec.Command("git", append(append([]string{}, options...), "rev-parse", "--git-dir")...)
	probe.Env = env
	if probe.Run() != nil {
		return nil
	} // clone from outside a repository
	c := exec.Command("git", append(append([]string{}, options...), "config", "--includes", "--null", "--list")...)
	c.Env = env
	out, err := c.Output()
	if err != nil {
		return errors.New("cannot inspect local Git authentication configuration")
	}
	for _, entry := range strings.Split(string(out), "\x00") {
		key, value, _ := strings.Cut(entry, "\n")
		key = strings.ToLower(key)
		if strings.HasPrefix(key, "http.") || strings.HasPrefix(key, "credential.") || strings.HasPrefix(key, "url.") || key == "core.askpass" || key == "core.sshcommand" || key == "core.gitproxy" {
			return errors.New("local Git HTTP/credential overrides are not allowed; use OS trust or GIT_SSL_CAINFO for the CA")
		}
		if strings.HasPrefix(key, "remote.") {
			if strings.HasSuffix(key, ".url") || strings.HasSuffix(key, ".pushurl") {
				if !validForgeClone(origin, value) {
					return errors.New("repository remote is outside the configured HTTPS Forge origin")
				}
			} else if strings.HasSuffix(key, ".proxy") || strings.HasSuffix(key, ".uploadpack") || strings.HasSuffix(key, ".receivepack") || strings.HasSuffix(key, ".vcs") {
				return errors.New("local Git remote transport overrides are not allowed")
			}
		}
	}
	return nil
}

func authenticatedGit(args []string) *authGitCommand {
	c := &authGitCommand{Cmd: exec.Command("git"), cleanup: func() {}}
	fail := func(err error) *authGitCommand { c.preparationError = err; return c }
	p, err := savedProfile()
	if err != nil {
		return fail(err)
	}
	options, command, err := gitArguments(args, p.ForgeURL)
	if err != nil {
		return fail(err)
	}
	// Refuse any Git operation without a valid current OIDC session, even when
	// the remote might accept a different ambient authentication mechanism.
	if _, err := currentAccess(p.ForgeURL); err != nil {
		return fail(err)
	}
	d, err := authDir()
	if err != nil {
		return fail(err)
	}
	processHome, err := os.MkdirTemp(d, ".git-process-")
	if err != nil {
		return fail(errors.New("cannot isolate Git authentication"))
	}
	c.cleanup = func() { os.RemoveAll(processHome) }
	if err := os.Mkdir(filepath.Join(processHome, "template"), 0700); err != nil {
		return fail(errors.New("cannot create empty Git template"))
	}
	env := cleanGitEnv()
	for k, v := range map[string]string{"HOME": processHome, "USERPROFILE": processHome, "XDG_CONFIG_HOME": filepath.Join(processHome, ".config"), "CURL_HOME": processHome, "_BRAIN_GIT_AUTH_DIR": d} {
		env = gitEnvSet(env, k, v)
	}
	if command != "clone" {
		if err := checkGitLocalConfig(options, env, p.ForgeURL); err != nil {
			return fail(err)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return fail(errors.New("cannot locate Brain credential helper"))
	}
	helper := "!'" + strings.ReplaceAll(filepath.ToSlash(exe), "'", "'\\''") + "' git-credential"
	prefix := []string{"--no-pager", "-c", "credential.helper=", "-c", "credential.helper=" + helper, "-c", "credential.useHttpPath=true", "-c", "http.followRedirects=false", "-c", "http.sslVerify=true", "-c", "http.extraHeader=", "-c", "http.saveCookies=false", "-c", "http.emptyAuth=false", "-c", "http.delegation=none", "-c", "core.hooksPath=" + filepath.Join(processHome, "hooks"), "-c", "fetch.recurseSubmodules=false", "-c", "submodule.recurse=false", "-c", "push.recurseSubmodules=no"}
	safeArgs := append([]string{}, args...)
	if command == "clone" {
		at := len(options) + 1
		safeArgs = append(append(append([]string{}, args[:at]...), "--template="+filepath.Join(processHome, "template")), args[at:]...)
	}
	c.Cmd = exec.Command("git", append(prefix, safeArgs...)...)
	c.Env = env
	return c
}

func cmdGit(args []string) int {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: brain git -- <git arguments>")
		return 2
	}
	c := authenticatedGit(args)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			return e.ExitCode()
		}
		return authError(err)
	}
	return 0
}
