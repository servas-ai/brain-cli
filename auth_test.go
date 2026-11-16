package main

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Only the test executable supports a synthetic issuer; this dispatch does not
// exist in any built customer binary. Real Git calls the real credential helper.
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "git-credential" && os.Getenv("BRAIN_TEST_HELPER") == "1" {
		testIssuerAllowed = true
		code := cmdGitCredential(os.Args[2:])
		if code != 0 {
			p, e := savedProfile()
			if e != nil {
				fmt.Fprintln(os.Stderr, "test helper profile:", e)
			} else {
				_, e = currentAccess(p.ForgeURL)
				fmt.Fprintln(os.Stderr, "test helper access:", e)
			}
		}
		os.Exit(code)
	}
	os.Exit(m.Run())
}

func TestAuthStorageRejectsPublicFilesAndSymlinks(t *testing.T) {
	isolatedGit(t)
	if err := writeAuthFile("session.json", map[string]string{"test": "synthetic"}); err != nil {
		t.Fatal(err)
	}
	d, err := authDir()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, "session.json")
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0600 {
		t.Fatal("session file must be private")
	}
	os.Chmod(p, 0644)
	var v map[string]string
	if readAuthFile("session.json", &v) == nil {
		t.Fatal("public token file accepted")
	}
	os.Remove(p)
	target := filepath.Join(t.TempDir(), "other")
	os.WriteFile(target, []byte(`{}`), 0600)
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
	if readAuthFile("session.json", &v) == nil {
		t.Fatal("symlink token file accepted")
	}
	if err := writeAuthFile("session.json", map[string]string{"test": "replacement"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(target)
	if string(b) != `{}` {
		t.Fatal("write followed symlink")
	}
}

func TestAuthFreshStateDoesNotReadPAT(t *testing.T) {
	home, _ := isolatedGit(t)
	os.WriteFile(filepath.Join(home, ".git-credentials"), []byte("https://old:synthetic-pat@git.example.invalid\n"), 0600)
	if _, _, err := gitCredential("https://git.example.invalid"); err == nil {
		t.Fatal("credential store used without OIDC session")
	}
	if cmdLogin([]string{"--no-open"}) != 2 {
		t.Fatal("obsolete PAT mode accepted")
	}
	if cmdAuth([]string{"status"}) != 1 {
		t.Fatal("fresh state authenticated")
	}
}

func TestAuthStorageRejectsRepositoryConfigLocations(t *testing.T) {
	for _, bare := range []bool{false, true} {
		name := "worktree"
		if bare {
			name = "bare"
		}
		t.Run(name, func(t *testing.T) {
			home, _ := isolatedGit(t)
			args := []string{"init", "-q"}
			if bare {
				args = append(args, "--bare")
			}
			mustGit(t, append(args, home)...)
			if err := writeAuthFile("session.json", map[string]string{"test": "synthetic"}); err == nil {
				t.Fatal("repository accepted as authentication store")
			}
			config, err := os.UserConfigDir()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(config, "brain", "auth")); !os.IsNotExist(err) {
				t.Fatal("unsafe store created before repository rejection")
			}
		})
	}
}

func TestAuthStorageRejectsRepositoryCreatedAfterLogin(t *testing.T) {
	home, _ := isolatedGit(t)
	if err := writeAuthFile("session.json", map[string]string{"test": "synthetic"}); err != nil {
		t.Fatal(err)
	}
	mustGit(t, "init", "-q", home)
	var session map[string]string
	if readAuthFile("session.json", &session) == nil {
		t.Fatal("session read after config directory became a repository")
	}
	if err := writeAuthFile("session.json", map[string]string{"test": "replacement"}); err == nil {
		t.Fatal("session updated inside a repository")
	}
}

func TestForgeCloneURLBoundToOrigin(t *testing.T) {
	for _, bad := range []string{"http://git.example.invalid/a.git", "https://other.invalid/a.git", "https://user:secret@git.example.invalid/a.git", "https://git.example.invalid/a.git?token=x", "file:///a", "-c x=y"} {
		if validForgeClone("https://git.example.invalid", bad) {
			t.Fatal("unsafe clone URL accepted")
		}
	}
	if !validForgeClone("https://git.example.invalid", "https://git.example.invalid/brain/brain.git") {
		t.Fatal("valid clone rejected")
	}
}

func TestGitOIDCCloneFetchPushAndProposalTLS(t *testing.T) {
	home, _ := isolatedGit(t)
	f := newOIDCFixture(t)
	// Default TLS transport in this test trusts only its local synthetic IdP.
	oldTransport := http.DefaultTransport
	http.DefaultTransport = f.server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	caFile := filepath.Join(t.TempDir(), "fixture-ca.pem")
	cert := f.server.Certificate()
	if _, err := x509.ParseCertificate(cert.Raw); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0600)
	t.Setenv("SSL_CERT_FILE", caFile)
	t.Setenv("GIT_SSL_CAINFO", caFile)
	t.Setenv("BRAIN_TEST_HELPER", "1")
	// A pre-existing store must never receive the OIDC access token.
	os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[credential]\n helper = store\n"), 0600)
	projects := t.TempDir()
	bare := filepath.Join(projects, "brain", "demo.git")
	mustGit(t, "init", "--bare", "-q", bare)
	mustGit(t, "--git-dir="+bare, "config", "http.receivepack", "true")
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	var forge *httptest.Server
	forge = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var token string
		if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			token = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		} else {
			user, pass, ok := r.BasicAuth()
			if ok && user == "oidc" {
				token = pass
			}
		}
		if token == "" {
			w.Header().Set("WWW-Authenticate", `Basic realm="fixture-oidc"`)
			w.WriteHeader(401)
			return
		}
		sub, _, err := verifyAccess(f.ctx, f.profile, token)
		if err != nil || sub != "tls-fixture-subject" {
			w.WriteHeader(403)
			return
		}
		switch {
		case r.URL.Path == "/api/v1/repos/brain/demo/pulls" && r.Method == "POST":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(201)
			io.WriteString(w, `{"html_url":"`+forge.URL+`/brain/demo/pulls/1"}`)
		case strings.HasPrefix(r.URL.Path, "/brain/demo.git/"):
			(&cgi.Handler{Path: gitBin, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + projects, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=alice"}}).ServeHTTP(w, r)
		default:
			w.WriteHeader(403)
		}
	}))
	defer forge.Close()
	f.profile.ForgeURL = forge.URL
	s, err := loginOIDC(f.ctx, f.profile, f.browser(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAuthFile("profile.json", f.profile); err != nil {
		t.Fatal(err)
	}
	if err := writeAuthFile("session.json", s); err != nil {
		t.Fatal(err)
	}
	if _, err := currentAccess(forge.URL); err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	hc := exec.Command(exe, "git-credential", "get")
	hc.Env = cleanGitEnv()
	u, _ := url.Parse(forge.URL)
	hc.Stdin = strings.NewReader("protocol=https\nhost=" + u.Host + "\n\n")
	hout, he := hc.CombinedOutput()
	if he != nil {
		t.Fatalf("helper process failed: %v %s", he, hout)
	}
	if !strings.Contains(string(hout), "password=") {
		t.Fatal("helper did not return a credential")
	}
	work := filepath.Join(t.TempDir(), "clone")
	run := func(args ...string) {
		t.Helper()
		c := authenticatedGit(args)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("Git %s failed: %v\n%s", args[0], err, out)
		}
	}
	run("clone", forge.URL+"/brain/demo.git", work)
	run("-C", work, "switch", "-c", "proposal/first-change")
	os.WriteFile(filepath.Join(work, "README.md"), []byte("Synthetic first proposal\n"), 0644)
	run("-C", work, "add", "README.md")
	run("-C", work, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "First proposal")
	run("-C", work, "push", "-u", "origin", "proposal/first-change")
	run("-C", work, "fetch", "origin")

	// A branch/default-push URL must not bypass the inspected named remotes.
	for _, key := range []string{"branch.proposal/first-change.remote", "branch.proposal/first-change.pushRemote", "remote.pushDefault"} {
		mustGit(t, "-C", work, "config", key, "https://foreign.invalid/brain/foreign.git")
		c := authenticatedGit([]string{"-C", work, "pull", "--ff-only"})
		if c.preparationError == nil {
			c.cleanup()
			t.Fatal("direct upstream URL passed preflight")
		}
		c.cleanup()
		mustGit(t, "-C", work, "config", "--unset", key)
	}
	mustGit(t, "-C", work, "config", "branch.proposal/first-change.remote", "origin")

	for _, legacy := range []string{"remotes", "branches"} {
		dir := filepath.Join(work, ".git", legacy)
		os.Mkdir(dir, 0700)
		file := filepath.Join(dir, "hidden")
		os.WriteFile(file, []byte("URL: https://foreign.invalid/a.git\n"), 0600)
		c := authenticatedGit([]string{"-C", work, "fetch", "hidden"})
		if c.preparationError == nil {
			c.cleanup()
			t.Fatal("legacy remote metadata passed preflight")
		}
		c.cleanup()
		os.Remove(file)
		os.Remove(dir)
	}
	for _, key := range []string{"remotes.hidden", "fetch.bundleURI"} {
		mustGit(t, "-C", work, "config", key, "https://foreign.invalid/a.git")
		c := authenticatedGit([]string{"-C", work, "fetch", "origin"})
		if c.preparationError == nil {
			c.cleanup()
			t.Fatal("indirect remote configuration passed preflight")
		}
		c.cleanup()
		mustGit(t, "-C", work, "config", "--unset", key)
	}
	for _, argv := range [][]string{{"fetch", "hidden"}, {"push", "hidden", "HEAD:main"}, {"fetch", "--multiple", "origin"}, {"push", "--repo=https://foreign.invalid/a.git"}} {
		c := authenticatedGit(append([]string{"-C", work}, argv...))
		if c.preparationError == nil {
			c.cleanup()
			t.Fatal("uninspected network destination passed preflight")
		}
		c.cleanup()
	}
	// Legacy commands without forge.yml still use the verified session.
	if err := git("-C", work, "pull", "--ff-only"); err != nil {
		t.Fatalf("OIDC pull without Forge marker failed: %v", err)
	}
	mustGit(t, "--git-dir="+bare, "symbolic-ref", "HEAD", "refs/heads/proposal/first-change")
	cellRoot := t.TempDir()
	if cmdCloneCell([]string{"--root", cellRoot, forge.URL + "/brain/demo.git"}) != 0 {
		t.Fatal("OIDC clone-cell without Forge marker failed")
	}
	if !exists(filepath.Join(cellRoot, "workspaces", "demo", "README.md")) {
		t.Fatal("OIDC clone-cell did not check out the proposal")
	}
	oldHTTP := forgeHTTP
	forgeHTTP = forge.Client()
	forgeHTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	defer func() { forgeHTTP = oldHTTP }()
	if _, err := createProposal(forge.URL, s.AccessToken, "brain/demo", "proposal/first-change", "main", "First proposal", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := createProposal(forge.URL, s.AccessToken, "brain/foreign", "proposal/first-change", "main", "Denied", ""); err == nil {
		t.Fatal("foreign PR accepted")
	}
	if out, err := authenticatedGit([]string{"ls-remote", forge.URL + "/brain/foreign.git"}).CombinedOutput(); err == nil {
		t.Fatal("foreign repository accepted")
	} else if strings.Contains(string(out), s.AccessToken) {
		t.Fatal("credential leaked to Git output")
	}
	if exists(filepath.Join(home, ".git-credentials")) {
		t.Fatal("OIDC token copied into Git store")
	}
	// The server checks subject, rather than trusting an email or Git username.
	foreign := f.sign(f.profile.Issuer, f.profile.Audience, "other-subject", "", false)
	if _, err := createProposal(forge.URL, foreign, "brain/demo", "p", "main", "Denied", ""); err == nil {
		t.Fatal("foreign identity accepted")
	}
	// Cached tokens cannot bypass the current audience or Forge binding.
	if _, err := currentAccess("https://other.invalid"); err == nil {
		t.Fatal("foreign origin accepted")
	}
	s.Expiry = time.Now().Add(-time.Minute)
	writeAuthFile("session.json", s)
	if _, err := currentAccess(forge.URL); err != nil || f.refreshes != 1 {
		t.Fatal("automatic refresh failed")
	}
	if cmdAuth([]string{"logout"}) != 0 {
		t.Fatal("logout failed")
	}
	if _, err := currentAccess(forge.URL); err == nil {
		t.Fatal("logged-out session accepted")
	}
	if len(f.revoked) != 2 {
		t.Fatal("logout did not revoke both credentials")
	}
}

func TestGitEnvironmentDoesNotEnableCredentialTracing(t *testing.T) {
	t.Setenv("GIT_TRACE_REDACT", "0")
	t.Setenv("GIT_TRACE_CURL", "1")
	t.Setenv("GIT_SSL_NO_VERIFY", "true")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "http.sslVerify")
	t.Setenv("GIT_CONFIG_VALUE_0", "false")
	for _, entry := range cleanGitEnv() {
		if strings.HasPrefix(entry, "GIT_TRACE") && entry != "GIT_TRACE_REDACT=1" {
			t.Fatal("trace inherited")
		}
		if (strings.HasPrefix(entry, "GIT_CONFIG_") && entry != "GIT_CONFIG_NOSYSTEM=1" && entry != "GIT_CONFIG_GLOBAL="+os.DevNull && entry != "GIT_CONFIG_SYSTEM="+os.DevNull) || strings.HasPrefix(entry, "GIT_SSL_NO_VERIFY=") {
			t.Fatal("unsafe Git environment inherited")
		}
	}
}

func TestLogoutRevokesOriginalSessionAfterProfileChange(t *testing.T) {
	isolatedGit(t)
	f := newOIDCFixture(t)
	old := http.DefaultTransport
	http.DefaultTransport = f.server.Client().Transport
	defer func() { http.DefaultTransport = old }()
	s, err := loginOIDC(f.ctx, f.profile, f.browser(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAuthFile("session.json", s); err != nil {
		t.Fatal(err)
	}
	p := f.profile
	p.ClientID = "another-client"
	if err := writeAuthFile("profile.json", p); err != nil {
		t.Fatal(err)
	}
	if cmdAuth([]string{"logout"}) != 0 {
		t.Fatal("logout after profile change failed")
	}
	if len(f.revoked) != 2 {
		t.Fatal("original tokens not revoked")
	}
	var loaded oidcSession
	if readAuthFile("session.json", &loaded) == nil {
		t.Fatal("session remains after logout")
	}
}
