package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func activateGitFixture(t *testing.T, f *oidcFixture, origin string) *oidcSession {
	t.Helper()
	old := http.DefaultTransport
	http.DefaultTransport = f.server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = old })
	ca := filepath.Join(t.TempDir(), "fixture-ca.pem")
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.server.Certificate().Raw}), 0600)
	t.Setenv("SSL_CERT_FILE", ca)
	t.Setenv("GIT_SSL_CAINFO", ca)
	t.Setenv("BRAIN_TEST_HELPER", "1")
	f.profile.ForgeURL = origin
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
	return s
}

// Regression from independent review: a valid OIDC session must not allow
// ambient credentials to silently select a different identity in libcurl.
func TestGitIsolatesAmbientCredentialsWithValidSession(t *testing.T) {
	for _, mode := range []string{"netrc", "scoped-header", "scoped-cookie"} {
		t.Run(mode, func(t *testing.T) {
			home, _ := isolatedGit(t)
			f := newOIDCFixture(t)
			var expected string
			var ambient, oidc atomic.Bool
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user, pass, ok := r.BasicAuth()
				if ok && (user == "old-user" || pass == "synthetic-old-pat") || strings.Contains(r.Header.Get("Cookie"), "synthetic-cookie") {
					ambient.Store(true)
				}
				if !ok || user != "oidc" || pass != expected {
					w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
					w.WriteHeader(401)
					return
				}
				oidc.Store(true)
				io.WriteString(w, "invalid Git packet after verified authentication")
			}))
			defer server.Close()
			s := activateGitFixture(t, f, server.URL)
			expected = s.AccessToken
			u, _ := url.Parse(server.URL)
			switch mode {
			case "netrc":
				os.WriteFile(filepath.Join(home, ".netrc"), []byte("machine "+u.Hostname()+" login old-user password synthetic-old-pat\n"), 0600)
			case "scoped-header":
				req, _ := http.NewRequest("GET", server.URL, nil)
				req.SetBasicAuth("old-user", "synthetic-old-pat")
				os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(fmt.Sprintf("[http %q]\n extraHeader = Authorization: %s\n", server.URL, req.Header.Get("Authorization"))), 0600)
			case "scoped-cookie":
				cookies := filepath.Join(home, "cookies")
				os.WriteFile(cookies, []byte("127.0.0.1\tFALSE\t/\tTRUE\t2147483647\tsession\tsynthetic-cookie\n"), 0600)
				os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(fmt.Sprintf("[http %q]\n cookieFile = %s\n", server.URL, cookies)), 0600)
			}
			out, _ := authenticatedGit([]string{"-C", t.TempDir(), "ls-remote", server.URL + "/brain/demo.git"}).CombinedOutput()
			if ambient.Load() {
				t.Fatal("ambient credential reached Forge")
			}
			if !oidc.Load() {
				t.Fatalf("OIDC credential was not used: %s", out)
			}
			if strings.Contains(string(out), s.AccessToken) {
				t.Fatal("OIDC credential leaked to output")
			}
			d, _ := authDir()
			entries, _ := os.ReadDir(d)
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".git-process-") {
					t.Fatal("temporary Git home remains")
				}
			}
		})
	}
}

func TestGitRejectsLocalHTTPOverridesWithValidSession(t *testing.T) {
	for _, key := range []string{"extraHeader", "cookieFile", "sslVerify"} {
		t.Run(key, func(t *testing.T) {
			isolatedGit(t)
			f := newOIDCFixture(t)
			var reached atomic.Bool
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true); w.WriteHeader(401) }))
			defer server.Close()
			activateGitFixture(t, f, server.URL)
			repo := t.TempDir()
			mustGit(t, "init", "-q", repo)
			mustGit(t, "-C", repo, "config", "http."+server.URL+"."+key, "false")
			_, err := authenticatedGit([]string{"-C", repo, "ls-remote", server.URL + "/brain/demo.git"}).CombinedOutput()
			if err == nil || reached.Load() {
				t.Fatal("local HTTP override was not rejected before network Git")
			}
		})
	}
}

func TestGitScopedTLSBypassIgnoredWithValidSession(t *testing.T) {
	home, _ := isolatedGit(t)
	f := newOIDCFixture(t)
	var reached atomic.Bool
	// A different local CA is deliberately absent from Git's trusted fixture CA.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(100), Subject: pkix.Name{CommonName: "untrusted-git-fixture"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true); w.WriteHeader(200) }))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()
	activateGitFixture(t, f, server.URL)
	os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(fmt.Sprintf("[http %q]\n sslVerify = false\n", server.URL)), 0600)
	_, err = authenticatedGit([]string{"-C", t.TempDir(), "ls-remote", server.URL + "/brain/demo.git"}).CombinedOutput()
	if err == nil || reached.Load() {
		t.Fatal("untrusted TLS reached Forge via ambient bypass")
	}
}

func TestGitArgumentsRejectAuthenticationOverrides(t *testing.T) {
	for _, args := range [][]string{{"-c", "http.sslVerify=false", "fetch"}, {"clone", "--config=http.extraHeader=secret", "https://git.example.invalid/a.git"}, {"clone", "https://name:password@git.example.invalid/a.git"}, {"clone", "ssh://git@git.example.invalid/a.git"}, {"arbitrary-alias"}} {
		if _, _, err := gitArguments(args, "https://git.example.invalid"); err == nil {
			t.Fatal("unsafe Git arguments accepted")
		}
	}
}
