package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Regression from independent review: explicit clone templates must not re-enable
// alternate HTTP credentials after the wrapper's authentication isolation.
func TestGitCloneTemplateCredentialBoundary(t *testing.T) {
	isolatedGit(t)
	f := newOIDCFixture(t)
	var alternate atomic.Bool
	forge := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Authorization"), base64.StdEncoding.EncodeToString([]byte("foreign-user:synthetic-template-pat"))) {
			alternate.Store(true)
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="review-two"`)
		w.WriteHeader(401)
	}))
	defer forge.Close()
	activateGitFixture(t, f, forge.URL)
	template := t.TempDir()
	config := fmt.Sprintf("[http %q]\n extraHeader = Authorization: Basic %s\n", forge.URL, base64.StdEncoding.EncodeToString([]byte("foreign-user:synthetic-template-pat")))
	if err := os.WriteFile(filepath.Join(template, "config"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	c := authenticatedGit([]string{"-C", t.TempDir(), "clone", "--template=" + template, forge.URL + "/brain/demo.git", filepath.Join(t.TempDir(), "clone")})
	out, err := c.CombinedOutput()
	t.Logf("wrapper_result=%v alternative_credential_reached_forge=%v", err, alternate.Load())
	if alternate.Load() || err == nil {
		t.Fatalf("clone template was not rejected safely; Git output: %s", out)
	}
}

func TestGitCloneCurrentRepoCredentialBoundary(t *testing.T) {
	isolatedGit(t)
	f := newOIDCFixture(t)
	var alternate atomic.Bool
	forge := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Authorization"), base64.StdEncoding.EncodeToString([]byte("foreign-user:synthetic-cwd-pat"))) {
			alternate.Store(true)
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="review-two"`)
		w.WriteHeader(401)
	}))
	defer forge.Close()
	activateGitFixture(t, f, forge.URL)
	repo := t.TempDir()
	mustGit(t, "init", "-q", repo)
	mustGit(t, "-C", repo, "config", "http."+forge.URL+".extraHeader", "Authorization: Basic "+base64.StdEncoding.EncodeToString([]byte("foreign-user:synthetic-cwd-pat")))
	c := authenticatedGit([]string{"-C", repo, "clone", forge.URL + "/brain/demo.git", filepath.Join(t.TempDir(), "clone")})
	_, err := c.CombinedOutput()
	t.Logf("wrapper_result=%v alternative_credential_reached_forge=%v", err, alternate.Load())
	if alternate.Load() {
		t.Fatal("clone working repository bypassed OIDC credential isolation")
	}
}

func TestGitCloneTemplateTLSBoundary(t *testing.T) {
	isolatedGit(t)
	f := newOIDCFixture(t)
	var reached, credential atomic.Bool
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(73502), Subject: pkix.Name{CommonName: "review-two-untrusted"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	var expected string
	forge := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
		user, pass, ok := r.BasicAuth()
		if ok && user == "oidc" && pass == expected {
			credential.Store(true)
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="review-two"`)
		w.WriteHeader(401)
	}))
	forge.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	forge.StartTLS()
	defer forge.Close()
	session := activateGitFixture(t, f, forge.URL)
	expected = session.AccessToken
	template := t.TempDir()
	if err := os.WriteFile(filepath.Join(template, "config"), []byte(fmt.Sprintf("[http %q]\n sslVerify = false\n", forge.URL)), 0600); err != nil {
		t.Fatal(err)
	}
	c := authenticatedGit([]string{"-C", t.TempDir(), "clone", "--template=" + template, forge.URL + "/brain/demo.git", filepath.Join(t.TempDir(), "clone")})
	_, err = c.CombinedOutput()
	t.Logf("wrapper_result=%v untrusted_tls_reached=%v oidc_credential_reached_untrusted_server=%v", err, reached.Load(), credential.Load())
	if reached.Load() {
		t.Fatal("clone template disabled TLS verification")
	}
}
