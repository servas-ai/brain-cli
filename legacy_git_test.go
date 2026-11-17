package main

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// Probe the separately exposed clone-cell entrypoint with no Forge marker/profile.
func TestLegacyCloneCellRejectsAmbientCredentials(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	root := filepath.Join(dir, "root")
	os.MkdirAll(home, 0700)
	os.MkdirAll(root, 0700)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[credential]\n helper = \"!f() { echo username=review; echo password=ambient-review-four; }; f\"\n"), 0600)
	var ambientUsed atomic.Bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if ok && user == "review" && pass == "ambient-review-four" {
			ambientUsed.Store(true)
			w.WriteHeader(403)
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="synthetic review"`)
		w.WriteHeader(401)
	}))
	defer srv.Close()
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	ca := filepath.Join(dir, "ca.pem")
	os.WriteFile(ca, cert, 0600)
	t.Setenv("GIT_SSL_CAINFO", ca)
	prior := forgeGitMode
	forgeGitMode = false
	defer func() { forgeGitMode = prior }()
	code := cmdCloneCell([]string{"--root", root, "--name", "review", srv.URL + "/review.git"})
	t.Logf("clone-cell exit=%d; ambient Basic credential received by isolated TLS fixture=%t", code, ambientUsed.Load())
	if ambientUsed.Load() || code != 1 {
		t.Fatal("clone-cell must reject missing OIDC before using ambient credentials")
	}
}

func TestLegacySyncRejectsAmbientCredentials(t *testing.T) {
	legacySyncRejectsAmbientCredentials(t, false)
}

func TestLegacySyncBranchURLRejectsAmbientCredentials(t *testing.T) {
	legacySyncRejectsAmbientCredentials(t, true)
}

func legacySyncRejectsAmbientCredentials(t *testing.T, branchURL bool) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	root := filepath.Join(dir, "root")
	os.MkdirAll(home, 0700)
	os.MkdirAll(root, 0700)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[credential]\n helper = \"!f() { echo username=review; echo password=ambient-review-four; }; f\"\n"), 0600)
	var ambientUsed atomic.Bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if ok && user == "review" && pass == "ambient-review-four" {
			ambientUsed.Store(true)
			w.WriteHeader(403)
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="synthetic review"`)
		w.WriteHeader(401)
	}))
	defer srv.Close()
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	ca := filepath.Join(dir, "ca.pem")
	os.WriteFile(ca, cert, 0600)
	t.Setenv("GIT_SSL_CAINFO", ca)
	mustGit(t, "init", "-q", "-b", "main", root)
	mustGit(t, "-C", root, "remote", "add", "origin", srv.URL+"/review.git")
	mustGit(t, "-C", root, "config", "branch.main.remote", "origin")
	mustGit(t, "-C", root, "config", "branch.main.merge", "refs/heads/main")
	if branchURL {
		mustGit(t, "-C", root, "remote", "set-url", "origin", "https://github.com/servas-ai/brain.git")
		mustGit(t, "-C", root, "config", "branch.main.remote", srv.URL+"/review.git")
	}
	prior := forgeGitMode
	forgeGitMode = false
	defer func() { forgeGitMode = prior }()
	code := cmdSync([]string{"--root", root, "--no-discover", "--no-update-check", "--no-katalog"})
	t.Logf("sync exit=%d; ambient Basic credential received by isolated TLS fixture=%t", code, ambientUsed.Load())
	if ambientUsed.Load() || code != 1 {
		t.Fatal("sync must reject missing OIDC before using ambient credentials")
	}
}

func TestLegacyGitHubDestinationBoundary(t *testing.T) {
	_, config := isolatedGit(t)
	root := t.TempDir()
	mustGit(t, "init", "-q", root)
	mustGit(t, "-C", root, "remote", "add", "origin", "https://github.com/servas-ai/brain.git")
	for _, args := range [][]string{{"clone", "--no-checkout", "https://github.com/servas-ai/brain.git"}, {"-C", root, "pull", "--ff-only"}} {
		if brainGitNeedsOIDC(args) {
			t.Fatal("existing HTTPS GitHub route rejected")
		}
	}
	for _, raw := range []string{"https://github.com.evil.invalid/a/b", "https://evil.invalid/github.com/a/b", "https://user:secret@github.com/a/b", "git@github.com:a/b", "http://github.com/a/b", "https://github.com/a/b?token=secret"} {
		if !brainGitNeedsOIDC([]string{"clone", raw}) {
			t.Fatal("unsafe legacy clone accepted")
		}
	}
	if err := os.WriteFile(config, []byte("[url \"https://other.invalid/\"]\n insteadOf = https://github.com/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if !brainGitNeedsOIDC([]string{"clone", "https://github.com/servas-ai/brain.git"}) || !brainGitNeedsOIDC([]string{"-C", root, "pull", "--ff-only"}) {
		t.Fatal("GitHub insteadOf rewrite bypassed OIDC")
	}
}

func TestLegacyForgeCorruptOrMismatchedProfileFailsClosed(t *testing.T) {
	for _, name := range []string{"corrupt", "mismatched"} {
		t.Run(name, func(t *testing.T) {
			isolatedGit(t)
			root := t.TempDir()
			if name == "corrupt" {
				if err := writeAuthFile("profile.json", map[string]string{"issuer": "broken"}); err != nil {
					t.Fatal(err)
				}
			} else {
				p := oidcProfile{Issuer: humanIssuer, ClientID: "fixture-client", Audience: "fixture-project", ForgeURL: "https://other.invalid", Scopes: []string{"openid", "offline_access", "urn:zitadel:iam:org:project:id:fixture-project:aud"}}
				if err := writeAuthFile("profile.json", p); err != nil {
					t.Fatal(err)
				}
			}
			if got := cmdCloneCell([]string{"--root", root, "https://forge.invalid/brain/cell.git"}); got != 1 {
				t.Fatalf("clone-cell exit=%d", got)
			}
			if exists(filepath.Join(root, "workspaces")) {
				t.Fatal("rejected clone created destination")
			}
			mustGit(t, "init", "-q", root)
			mustGit(t, "-C", root, "remote", "add", "origin", "https://github.com/servas-ai/brain.git")
			cell := filepath.Join(root, "workspaces", "cell")
			mustGit(t, "init", "-q", cell)
			mustGit(t, "-C", cell, "remote", "add", "origin", "https://forge.invalid/brain/cell.git")
			if got := cmdSync([]string{"--root", root, "--no-discover", "--no-update-check", "--no-katalog"}); got != 1 {
				t.Fatalf("mixed sync exit=%d", got)
			}
		})
	}
}

func TestLegacyGitHubTransportHelperRejected(t *testing.T) {
	isolatedGit(t)
	root := t.TempDir()
	mustGit(t, "init", "-q", root)
	mustGit(t, "-C", root, "remote", "add", "origin", "https://github.com/servas-ai/brain.git")
	mustGit(t, "-C", root, "config", "remote.origin.vcs", "unsafe-helper")
	if !brainGitNeedsOIDC([]string{"-C", root, "pull", "--ff-only"}) {
		t.Fatal("remote helper bypassed GitHub origin boundary")
	}
}
