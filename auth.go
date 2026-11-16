package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const humanIssuer = "https://auth.servas.ai"

func authDir() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", errors.New("cannot locate user configuration")
	}
	d = filepath.Join(d, "brain", "auth")
	if err := os.MkdirAll(d, 0700); err != nil {
		return "", errors.New("cannot create authentication directory")
	}
	// Never traverse symlinks into a different token store.
	for p := d; ; p = filepath.Dir(p) {
		st, err := os.Lstat(p)
		if err != nil || st.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("unsafe authentication directory")
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	st, err := os.Stat(d)
	if err != nil || !authPrivateMode(st) {
		return "", errors.New("authentication directory must be private (0700)")
	}
	return d, nil
}

func readAuthFile(name string, v any) error {
	d, err := authDir()
	if err != nil {
		return err
	}
	p := filepath.Join(d, name)
	f, err := os.OpenFile(p, os.O_RDONLY, 0)
	if err != nil {
		return errors.New("no saved authentication; configure and log in first")
	}
	defer f.Close()
	st, err := f.Stat()
	ls, lerr := os.Lstat(p)
	if err != nil || lerr != nil || !st.Mode().IsRegular() || !os.SameFile(st, ls) || !authPrivateMode(st) {
		return errors.New("unsafe authentication file (expected regular 0600 file)")
	}
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return errors.New("invalid authentication file")
	}
	data, err = unsealAuthData(name, data)
	if err != nil || json.Unmarshal(data, v) != nil {
		return errors.New("invalid authentication file")
	}
	return nil
}

func writeAuthFile(name string, v any) error {
	d, err := authDir()
	if err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return errors.New("cannot encode authentication")
	}
	b, err = sealAuthData(name, b)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(d, ".pending-")
	if err != nil {
		return errors.New("cannot save authentication")
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err == nil {
		err = os.Rename(f.Name(), filepath.Join(d, name))
	}
	if err != nil {
		return errors.New("cannot save authentication")
	}
	return nil
}

func savedProfile() (oidcProfile, error) {
	var p oidcProfile
	err := readAuthFile("profile.json", &p)
	if err == nil {
		err = validateProfile(p)
	}
	return p, err
}

func matchingSession(p oidcProfile, s *oidcSession) bool {
	return s.Issuer == p.Issuer && s.ClientID == p.ClientID && s.Audience == p.Audience && s.ForgeURL == p.ForgeURL && s.Subject != ""
}

func authContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 3*time.Minute)
}

// currentAccess never falls back to PATs, Git stores, browser cookies or another issuer.
func currentAccess(forge string) (string, error) {
	p, err := savedProfile()
	if err != nil {
		return "", err
	}
	if forge != p.ForgeURL {
		return "", errors.New("Forge origin does not match the configured authentication profile")
	}
	var s oidcSession
	if err := readAuthFile("session.json", &s); err != nil {
		return "", err
	}
	if !matchingSession(p, &s) {
		return "", errors.New("authentication profile changed; log in again")
	}
	ctx, cancel := authContext()
	defer cancel()
	if time.Until(s.Expiry) < time.Minute {
		next, err := refreshOIDC(ctx, p, &s)
		if err != nil {
			return "", errors.New("session refresh failed; log in again")
		}
		if err := writeAuthFile("session.json", next); err != nil {
			return "", err
		}
		s = *next
	}
	sub, _, err := verifyAccess(ctx, p, s.AccessToken)
	if err != nil || sub != s.Subject {
		return "", errors.New("access token validation failed; log in again")
	}
	return s.AccessToken, nil
}

func authError(err error) int { fmt.Fprintln(os.Stderr, "brain auth:", err); return 1 }

func cmdAuth(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: brain auth configure|status|logout")
		return 2
	}
	switch args[0] {
	case "configure":
		fs := flag.NewFlagSet("auth configure", flag.ContinueOnError)
		client := fs.String("client-id", "", "Registered public native Zitadel client ID")
		aud := fs.String("audience", "", "Registered Forge resource audience")
		forge := fs.String("url", "", "HTTPS Forge origin with the OIDC adapter")
		scopes := fs.String("scopes", "openid offline_access", "Space-separated registered scopes, including the resource audience scope")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
			return 2
		}
		p := oidcProfile{Issuer: humanIssuer, ClientID: *client, Audience: *aud, ForgeURL: strings.TrimRight(*forge, "/"), Scopes: strings.Fields(*scopes)}
		if err := validateProfile(p); err != nil {
			return authError(err)
		}
		if err := writeAuthFile("profile.json", p); err != nil {
			return authError(err)
		}
		fmt.Println("Authentication profile saved. Run: brain login")
		return 0
	case "status":
		if len(args) != 1 {
			return 2
		}
		p, err := savedProfile()
		if err != nil {
			fmt.Println(`{"configured":false,"authenticated":false}`)
			return 1
		}
		_, err = currentAccess(p.ForgeURL)
		if err != nil {
			fmt.Println(`{"configured":true,"authenticated":false}`)
			return 1
		}
		fmt.Println(`{"configured":true,"authenticated":true}`)
		return 0
	case "logout":
		if len(args) != 1 {
			return 2
		}
		p, err := savedProfile()
		if err != nil {
			return authError(err)
		}
		var s oidcSession
		if err := readAuthFile("session.json", &s); err != nil {
			return authError(err)
		}
		if !matchingSession(p, &s) {
			return authError(errors.New("profile mismatch; local session retained for revocation"))
		}
		ctx, cancel := authContext()
		defer cancel()
		revokeErr := revokeOIDC(ctx, p, &s)
		d, err := authDir()
		if err != nil {
			return authError(err)
		}
		if err := os.Remove(filepath.Join(d, "session.json")); err != nil {
			return authError(errors.New("cannot remove local session"))
		}
		if revokeErr != nil {
			return authError(errors.New("local session removed; server revocation unconfirmed; existing access tokens remain valid until expiry"))
		}
		fmt.Println("Logged out. Local session removed and revocation accepted; existing access tokens expire according to server policy.")
		return 0
	default:
		fmt.Fprintln(os.Stderr, "Usage: brain auth configure|status|logout")
		return 2
	}
}

func cmdLogin(args []string) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	forge := fs.String("url", "", "Require this configured Forge origin")
	root := fs.String("root", "", "Read expected Forge origin from this Brain root")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return 2
	}
	p, err := savedProfile()
	if err != nil {
		return authError(err)
	}
	if *root != "" {
		fc, e := readForge(*root)
		if e != nil || fc == nil {
			return authError(errors.New("no valid Forge configuration at this root"))
		}
		if *forge != "" && *forge != fc.URL {
			return authError(errors.New("Forge origin mismatch"))
		}
		*forge = fc.URL
	}
	if *forge != "" && strings.TrimRight(*forge, "/") != p.ForgeURL {
		return authError(errors.New("Forge origin mismatch"))
	}
	ctx, cancel := authContext()
	defer cancel()
	fmt.Fprintln(os.Stderr, "Opening your browser for Zitadel login. Complete sign-in within three minutes.")
	s, err := loginOIDC(ctx, p, openBrowser)
	if err != nil {
		return authError(err)
	}
	if err := writeAuthFile("session.json", s); err != nil {
		return authError(err)
	}
	fmt.Println("Logged in. Use brain git for authenticated Clone, Fetch and Push.")
	return 0
}

// The credential helper is only called by the brain git subprocess. Never register
// it globally: Git may otherwise copy access tokens to an existing store helper.
func cmdGitCredential(args []string) int {
	if len(args) != 1 || args[0] != "get" {
		return 0
	}
	fields := map[string]string{}
	sc := bufio.NewScanner(io.LimitReader(os.Stdin, 16<<10))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			break
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return 1
		}
		// Git's extensible protocol repeats array fields such as capability[].
		// Only the single-valued authority fields influence credential release.
		if k != "protocol" && k != "host" && k != "username" && k != "path" {
			continue
		}
		if _, dup := fields[k]; dup {
			return 1
		}
		fields[k] = v
	}
	if sc.Err() != nil || fields["protocol"] != "https" {
		return 1
	}
	p, err := savedProfile()
	if err != nil {
		return 1
	}
	u, err := url.Parse(p.ForgeURL)
	if err != nil || fields["host"] != u.Host {
		return 1
	}
	if fields["username"] != "" && fields["username"] != "oidc" {
		return 1
	}
	token, err := currentAccess(p.ForgeURL)
	if err != nil {
		return 1
	}
	fmt.Fprintf(os.Stdout, "username=oidc\npassword=%s\n\n", token)
	return 0
}

func cleanGitEnv() []string {
	var out []string
	for _, s := range os.Environ() {
		k, _, _ := strings.Cut(s, "=")
		if strings.HasPrefix(k, "GIT_TRACE") || strings.HasPrefix(k, "GIT_CONFIG_") || k == "GIT_CURL_VERBOSE" || k == "GIT_ASKPASS" || k == "SSH_ASKPASS" || k == "GIT_SSL_NO_VERIFY" || k == "GIT_CONFIG" {
			continue
		}
		out = append(out, s)
	}
	return append(out, "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_TRACE_REDACT=1")
}

func authenticatedGit(args []string) *exec.Cmd {
	exe, _ := os.Executable()
	// Git runs helper commands through its shell on every supported platform.
	helper := "!" + "'" + strings.ReplaceAll(filepath.ToSlash(exe), "'", "'\\''") + "' git-credential"
	prefix := []string{"-c", "credential.helper=", "-c", "credential.helper=" + helper, "-c", "credential.useHttpPath=true", "-c", "http.followRedirects=false", "-c", "http.sslVerify=true", "-c", "http.extraHeader="}
	c := exec.Command("git", append(prefix, args...)...)
	c.Env = cleanGitEnv()
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
	if _, err := savedProfile(); err != nil {
		return authError(err)
	}
	c := authenticatedGit(args)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			return e.ExitCode()
		}
		return authError(errors.New("cannot run Git"))
	}
	return 0
}
