package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestGitHubCloneIsolatesAmbientTransport(t *testing.T) {
	for _, attack := range []string{"remote-url", "remote-helper", "template"} {
		t.Run(attack, func(t *testing.T) {
			home, config := isolatedGit(t)
			root := t.TempDir()
			marker := filepath.Join(home, "ambient-helper-used")
			bin := filepath.Join(home, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			// No real login or remote is contacted. These programs only detect
			// whether inherited Git credentials or custom transports execute.
			for _, name := range []string{"gh", "git-remote-brain-fixture", "credential-fixture"} {
				body := "#!/bin/sh\nprintf used > " + gitShellWord(marker) + "\nexit 1\n"
				if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			var foreign, connects atomic.Int32
			forge := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				foreign.Add(1)
				w.WriteHeader(403)
			}))
			defer forge.Close()
			// An inert local proxy refuses the canonical GitHub CONNECT before
			// DNS/TLS to any real server. It never forwards a request.
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "CONNECT" || r.Host != "github.com:443" {
					t.Errorf("unexpected proxy target %s %s", r.Method, r.Host)
				}
				connects.Add(1)
				w.WriteHeader(http.StatusBadGateway)
			}))
			defer proxy.Close()
			for _, key := range []string{"HTTPS_PROXY", "https_proxy"} {
				t.Setenv(key, proxy.URL)
			}
			t.Setenv("NO_PROXY", "")
			t.Setenv("no_proxy", "")
			t.Setenv("GIT_ALLOW_PROTOCOL", "brain-fixture")
			settings := fmt.Sprintf("[credential]\n helper = %s\n", filepath.Join(bin, "credential-fixture"))
			switch attack {
			case "remote-url":
				settings += fmt.Sprintf("[remote \"origin\"]\n url = %s/review.git\n", forge.URL)
			case "remote-helper":
				settings += "[remote \"origin\"]\n vcs = brain-fixture\n"
			case "template":
				template := filepath.Join(home, "template")
				os.Mkdir(template, 0700)
				os.WriteFile(filepath.Join(template, "config"), []byte(fmt.Sprintf("[remote \"origin\"]\n url = %s/review.git\n", forge.URL)), 0600)
				settings += fmt.Sprintf("[init]\n templateDir = %s\n", template)
				t.Setenv("GIT_TEMPLATE_DIR", template)
			}
			if err := os.WriteFile(config, []byte(settings), 0600); err != nil {
				t.Fatal(err)
			}
			if got := cmdCloneCell([]string{"--root", root, "https://github.com/brain-fixture/synthetic.git"}); got != 1 {
				t.Fatalf("inert proxy should reject clone, exit=%d", got)
			}
			if foreign.Load() != 0 || exists(marker) {
				t.Fatal("ambient transport, credentials or foreign origin used")
			}
			if connects.Load() == 0 {
				t.Fatal("canonical GitHub worker was not exercised")
			}
		})
	}
}
