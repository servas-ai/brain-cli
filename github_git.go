package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func gitShellWord(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// GitHub retains its existing gh identity, but never inherits Git transport
// configuration. Only the credential helper receives gh's original config
// location; Git itself gets an empty home, config and clone template.
func githubGit(args []string) *authGitCommand {
	c := &authGitCommand{Cmd: exec.Command("git"), cleanup: func() {}}
	fail := func(err error) *authGitCommand { c.preparationError = err; return c }
	options, command, err := gitArguments(args, "https://github.com")
	if err != nil {
		return fail(err)
	}
	gh, err := exec.LookPath("gh")
	if err != nil {
		return fail(errors.New("GitHub workflows require the existing authenticated gh CLI"))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fail(errors.New("cannot locate existing GitHub login configuration"))
	}
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		xdg = filepath.Join(home, ".config")
	}
	ghConfig := os.Getenv("GH_CONFIG_DIR")
	if ghConfig == "" {
		ghConfig = filepath.Join(xdg, "gh")
	}
	processHome, err := os.MkdirTemp("", "brain-github-process-")
	if err != nil {
		return fail(errors.New("cannot isolate GitHub transport"))
	}
	c.cleanup = func() { os.RemoveAll(processHome) }
	if err := os.Mkdir(filepath.Join(processHome, "template"), 0700); err != nil {
		return fail(errors.New("cannot create empty GitHub clone template"))
	}
	env := cleanGitEnv()
	for k, v := range map[string]string{"HOME": processHome, "USERPROFILE": processHome, "XDG_CONFIG_HOME": filepath.Join(processHome, ".config"), "CURL_HOME": processHome} {
		env = gitEnvSet(env, k, v)
	}
	if command != "clone" {
		if err := checkGitLocalConfig(options, env, "https://github.com"); err != nil {
			return fail(err)
		}
	}
	safeArgs := append([]string{}, args...)
	if command == "pull" {
		remote, err := githubPullRemote(options)
		if err != nil {
			return fail(err)
		}
		safeArgs = append(append(append([]string{}, options...), "pull", "--ff-only"), remote)
	}
	if command == "clone" {
		at := len(options) + 1
		safeArgs = append(append(append([]string{}, args[:at]...), "--template="+filepath.Join(processHome, "template")), args[at:]...)
	}
	helper := "!env HOME=" + gitShellWord(filepath.ToSlash(home)) + " XDG_CONFIG_HOME=" + gitShellWord(filepath.ToSlash(xdg)) + " GH_CONFIG_DIR=" + gitShellWord(filepath.ToSlash(ghConfig)) + " " + gitShellWord(filepath.ToSlash(gh)) + " auth git-credential"
	prefix := []string{"--no-pager", "-c", "credential.helper=", "-c", "credential.helper=" + helper, "-c", "credential.useHttpPath=true", "-c", "http.followRedirects=false", "-c", "http.sslVerify=true", "-c", "http.extraHeader=", "-c", "http.saveCookies=false", "-c", "http.emptyAuth=false", "-c", "http.delegation=none", "-c", "core.hooksPath=" + filepath.Join(processHome, "hooks"), "-c", "fetch.recurseSubmodules=false", "-c", "submodule.recurse=false", "-c", "push.recurseSubmodules=no"}
	c.Cmd = exec.Command("git", append(prefix, safeArgs...)...)
	c.Env = env
	return c
}
