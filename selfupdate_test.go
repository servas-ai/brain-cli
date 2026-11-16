package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// All update tests target an inert copy in their own temporary directory.
// No test passes os.Executable() or an installed CLI to the replacement code.
type selfUpdateFixture struct {
	t                 *testing.T
	u                 *selfUpdater
	server            *httptest.Server
	key               ed25519.PrivateKey
	r                 selfUpdateRelease
	sums, sig, binary []byte
	status            int
	redirect, abort   bool
	requests          int
}

func newSelfUpdateFixture(t *testing.T) *selfUpdateFixture {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "brain-copy")
	if err := os.WriteFile(exe, []byte("inert old fixture binary"), 0755); err != nil {
		t.Fatal(err)
	}
	f := &selfUpdateFixture{t: t, key: key, binary: []byte("inert new fixture binary")}
	f.u = &selfUpdater{Current: "v1.0.0", Exe: exe, StatePath: filepath.Join(dir, "state.json"), Key: pub, Token: func(context.Context) (string, error) { return "fixture-access-token", nil }}
	f.publish("v1.1.0")
	f.server = httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
	f.server.Config.ErrorLog = log.New(io.Discard, "", 0)
	f.server.StartTLS()
	t.Cleanup(f.server.Close)
	f.u.BaseURL = f.server.URL
	f.u.HTTP = f.server.Client()
	return f
}
func (f *selfUpdateFixture) publish(v string) {
	f.r.CLI = "brain"
	f.r.Version = v
	f.r.File.Name = "brain-" + runtime.GOOS + "-" + runtime.GOARCH
	f.r.File.SHA256 = selfUpdateHash(f.binary)
	f.r.File.Size = int64(len(f.binary))
	prefix := "/v1/brain/" + v + "/"
	f.r.File.URL = prefix + f.r.File.Name
	f.r.SumsURL = prefix + "SHA256SUMS"
	f.r.SignatureURL = prefix + "SHA256SUMS.sig"
	f.sums = []byte(fmt.Sprintf("%s  %s\n%s  brain-version-%s\n", f.r.File.SHA256, f.r.File.Name, selfUpdateHash([]byte(v+"\n")), v))
	f.sign()
}
func (f *selfUpdateFixture) sign() {
	f.sig = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(f.key, f.sums)) + "\n")
}
func (f *selfUpdateFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.requests++
	if r.Header.Get("Authorization") != "Bearer fixture-access-token" {
		f.t.Error("missing fixture authentication")
	}
	if f.status != 0 {
		http.Error(w, "secret-response-token https://secret.invalid/?token=do-not-display", f.status)
		return
	}
	if f.redirect {
		http.Redirect(w, r, "/do-not-follow", http.StatusTemporaryRedirect)
		return
	}
	switch r.URL.Path {
	case "/v1/brain/latest":
		_ = json.NewEncoder(w).Encode(f.r)
	case f.r.SumsURL:
		_, _ = w.Write(f.sums)
	case f.r.SignatureURL:
		_, _ = w.Write(f.sig)
	case f.r.File.URL:
		if f.abort {
			w.Header().Set("Content-Length", fmt.Sprint(len(f.binary)+100))
			_, _ = w.Write(f.binary[:3])
			return
		}
		_, _ = w.Write(f.binary)
	default:
		f.t.Errorf("unexpected fixture path %s", r.URL.Path)
		http.NotFound(w, r)
	}
}
func (f *selfUpdateFixture) unchanged() {
	f.t.Helper()
	b, err := os.ReadFile(f.u.Exe)
	if err != nil || string(b) != "inert old fixture binary" {
		f.t.Fatalf("fixture executable changed: %q %v", b, err)
	}
}
func TestSelfUpdateInstallRollbackAndReplay(t *testing.T) {
	f := newSelfUpdateFixture(t)
	latest, installed, err := f.u.run(context.Background(), true, false)
	if err != nil || !installed || latest != "v1.1.0" {
		t.Fatalf("install: %s %t %v", latest, installed, err)
	}
	b, _ := os.ReadFile(f.u.Exe)
	if string(b) != string(f.binary) {
		t.Fatal("new bytes missing")
	}
	if err := f.u.rollback(); err != nil {
		t.Fatal(err)
	}
	f.unchanged()
	if err := f.u.rollback(); err == nil {
		t.Fatal("rollback authorization reused")
	}
	// A rollback never lowers the highest authenticated version.
	f.publish("v1.0.1")
	if _, _, err := f.u.run(context.Background(), true, false); err == nil || !strings.Contains(err.Error(), "replay") {
		t.Fatalf("replay accepted: %v", err)
	}
	f.unchanged()
	// Auto mode skips the explicitly rolled-back version; explicit update can
	// retry it after the user has inspected the failed release.
	f.publish("v1.1.0")
	if _, _, err := f.u.run(context.Background(), true, true); err != nil {
		t.Fatal(err)
	}
	f.unchanged()
	if _, installed, err := f.u.run(context.Background(), true, false); err != nil || !installed {
		t.Fatalf("explicit reinstall: %t %v", installed, err)
	}
}
func TestSelfUpdateRejectsUntrustedReleases(t *testing.T) {
	cases := map[string]func(*selfUpdateFixture){
		"bad signature": func(f *selfUpdateFixture) { f.sig = []byte("invalid signature") },
		"wrong signing key": func(f *selfUpdateFixture) {
			_, key, _ := ed25519.GenerateKey(rand.Reader)
			f.sig = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, f.sums)))
		},
		"hash mismatch": func(f *selfUpdateFixture) { f.binary = []byte("untrusted replacement") },
		"unsigned version replay": func(f *selfUpdateFixture) {
			f.r.Version = "v9.0.0"
			p := "/v1/brain/v9.0.0/"
			f.r.File.URL = p + f.r.File.Name
			f.r.SumsURL = p + "SHA256SUMS"
			f.r.SignatureURL = p + "SHA256SUMS.sig"
		},
		"no version binding": func(f *selfUpdateFixture) {
			f.sums = []byte(fmt.Sprintf("%s  %s\n", f.r.File.SHA256, f.r.File.Name))
			f.sign()
		},
		"multiple signed versions": func(f *selfUpdateFixture) {
			f.sums = append(f.sums, []byte(fmt.Sprintf("%s  brain-version-v9.0.0\n", selfUpdateHash([]byte("v9.0.0\n"))))...)
			f.sign()
		},
		"duplicate signed artifact": func(f *selfUpdateFixture) {
			f.sums = append(f.sums, []byte(fmt.Sprintf("%s  %s\n", f.r.File.SHA256, f.r.File.Name))...)
			f.sign()
		},
		"older release":      func(f *selfUpdateFixture) { f.publish("v0.9.0") },
		"wrong cli":          func(f *selfUpdateFixture) { f.r.CLI = "different" },
		"wrong platform":     func(f *selfUpdateFixture) { f.r.File.Name = "brain-other-platform" },
		"different origin":   func(f *selfUpdateFixture) { f.r.File.URL = "https://other.invalid/secret?token=hidden" },
		"wrong version URL":  func(f *selfUpdateFixture) { f.r.SumsURL = "/v1/brain/v9.9.9/SHA256SUMS" },
		"query on artifact":  func(f *selfUpdateFixture) { f.r.File.URL += "?token=hidden" },
		"prerelease current": func(f *selfUpdateFixture) { f.u.Current = "v1.0.0-rc.1" },
		"own build":          func(f *selfUpdateFixture) { f.u.Current = "dev" },
		"prerelease target":  func(f *selfUpdateFixture) { f.publish("v2.0.0-rc.1") },
		"size mismatch":      func(f *selfUpdateFixture) { f.r.File.Size++ },
		"aborted download":   func(f *selfUpdateFixture) { f.abort = true },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newSelfUpdateFixture(t)
			change(f)
			_, _, err := f.u.run(context.Background(), true, false)
			if err == nil {
				t.Fatal("untrusted update accepted")
			}
			if strings.Contains(err.Error(), "hidden") {
				t.Fatal("secret URL leaked")
			}
			f.unchanged()
			if _, err := os.Stat(f.u.Exe + ".prev"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("rollback touched on rejected download")
			}
		})
	}
}
func TestSelfUpdateTransportAndAuthentication(t *testing.T) {
	for _, status := range []int{401, 403, 404, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f := newSelfUpdateFixture(t)
			f.status = status
			_, _, err := f.u.run(context.Background(), true, false)
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unsafe error: %v", err)
			}
			f.unchanged()
		})
	}
	t.Run("redirect", func(t *testing.T) {
		f := newSelfUpdateFixture(t)
		f.redirect = true
		_, _, err := f.u.run(context.Background(), true, false)
		if err == nil || f.requests != 1 {
			t.Fatalf("redirect followed: %v %d", err, f.requests)
		}
		f.unchanged()
	})
	t.Run("untrusted TLS", func(t *testing.T) {
		f := newSelfUpdateFixture(t)
		f.u.HTTP = nil
		_, _, err := f.u.run(context.Background(), true, false)
		if err == nil || f.requests != 0 {
			t.Fatalf("untrusted TLS accepted: %v", err)
		}
		f.unchanged()
	})
	t.Run("plain HTTP", func(t *testing.T) {
		f := newSelfUpdateFixture(t)
		f.u.BaseURL = "http://127.0.0.1"
		_, _, err := f.u.run(context.Background(), true, false)
		if err == nil || f.requests != 0 {
			t.Fatal("HTTP accepted")
		}
		f.unchanged()
	})
	t.Run("no token fallback", func(t *testing.T) {
		f := newSelfUpdateFixture(t)
		t.Setenv("GITHUB_TOKEN", "ambient-secret")
		t.Setenv("BRAIN_TOKEN", "ambient-secret")
		f.u.Token = func(context.Context) (string, error) { return "", errors.New("secret-provider-error") }
		_, _, err := f.u.run(context.Background(), true, false)
		if err == nil || strings.Contains(err.Error(), "secret-provider") || f.requests != 0 {
			t.Fatalf("auth fallback or leak: %v", err)
		}
		f.unchanged()
	})
	t.Run("canceled", func(t *testing.T) {
		f := newSelfUpdateFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err := f.u.run(ctx, true, false)
		if err == nil {
			t.Fatal("cancel ignored")
		}
		f.unchanged()
	})
}
func TestSelfUpdateFilesystemAndRollbackTamper(t *testing.T) {
	t.Run("binary symlink", func(t *testing.T) {
		f := newSelfUpdateFixture(t)
		link := filepath.Join(filepath.Dir(f.u.Exe), "link")
		if err := os.Symlink(f.u.Exe, link); err != nil {
			t.Fatal(err)
		}
		original := f.u.Exe
		f.u.Exe = link
		_, _, err := f.u.run(context.Background(), true, false)
		if err == nil {
			t.Fatal("binary symlink accepted")
		}
		f.u.Exe = original
		f.unchanged()
	})
	t.Run("unsafe binary mode", func(t *testing.T) {
		f := newSelfUpdateFixture(t)
		os.Chmod(f.u.Exe, 0777)
		if _, _, err := f.u.run(context.Background(), true, false); err == nil {
			t.Fatal("unsafe binary mode accepted")
		}
		f.unchanged()
	})
	t.Run("hardlinked binary", func(t *testing.T) {
		f := newSelfUpdateFixture(t)
		os.Link(f.u.Exe, f.u.Exe+".link")
		if _, _, err := f.u.run(context.Background(), true, false); err == nil {
			t.Fatal("hardlinked binary accepted")
		}
		f.unchanged()
	})
	t.Run("state symlink", func(t *testing.T) {
		f := newSelfUpdateFixture(t)
		os.Symlink(f.u.Exe, f.u.StatePath)
		if _, _, err := f.u.run(context.Background(), true, false); err == nil {
			t.Fatal("state symlink accepted")
		}
		f.unchanged()
	})
	t.Run("rollback symlink", func(t *testing.T) {
		f := newSelfUpdateFixture(t)
		os.Symlink(f.u.Exe, f.u.Exe+".prev")
		if _, _, err := f.u.run(context.Background(), true, false); err == nil {
			t.Fatal("rollback symlink accepted")
		}
		f.unchanged()
	})
	for _, tamper := range []string{"bytes", "symlink", "current"} {
		t.Run("rollback tamper "+tamper, func(t *testing.T) {
			f := newSelfUpdateFixture(t)
			if _, _, err := f.u.run(context.Background(), true, false); err != nil {
				t.Fatal(err)
			}
			switch tamper {
			case "bytes":
				os.WriteFile(f.u.Exe+".prev", []byte("tampered rollback"), 0755)
			case "symlink":
				os.Remove(f.u.Exe + ".prev")
				os.Symlink(f.u.Exe, f.u.Exe+".prev")
			case "current":
				os.WriteFile(f.u.Exe, []byte("tampered installed"), 0755)
			}
			before, _ := os.ReadFile(f.u.Exe)
			if err := f.u.rollback(); err == nil {
				t.Fatal("tampered rollback accepted")
			}
			after, _ := os.ReadFile(f.u.Exe)
			if string(before) != string(after) {
				t.Fatal("tampered rollback changed binary")
			}
		})
	}
}
func TestSelfUpdateSeparateHumanSession(t *testing.T) {
	isolatedGit(t)
	f := newOIDCFixture(t)
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
	t.Setenv("GITHUB_TOKEN", "ambient-pat-secret")
	if _, err := selfUpdateAccess(f.ctx, f.profile); err == nil {
		t.Fatal("Forge session used for update auth")
	}
	if err := writeAuthFile("update-profile.json", f.profile); err != nil {
		t.Fatal(err)
	}
	if err := writeAuthFile("update-session.json", s); err != nil {
		t.Fatal(err)
	}
	if _, err := selfUpdateAccess(f.ctx, f.profile); err != nil {
		t.Fatal(err)
	}
	s.Expiry = time.Now().Add(-time.Minute)
	if err := writeAuthFile("update-session.json", s); err != nil {
		t.Fatal(err)
	}
	if _, err := selfUpdateAccess(f.ctx, f.profile); err != nil {
		t.Fatal(err)
	}
	if f.refreshes != 1 {
		t.Fatal("update refresh missing")
	}
	s.Subject = "another-fixture-subject"
	s.Expiry = time.Now().Add(time.Hour)
	writeAuthFile("update-session.json", s)
	if _, err := selfUpdateAccess(f.ctx, f.profile); err == nil {
		t.Fatal("subject mismatch accepted")
	}
	// A production update client never adopts a saved fixture/Forge profile.
	if _, err := currentUpdateAccess(context.Background()); err == nil {
		t.Fatal("nonproduction update profile accepted")
	}
}
func TestSelfUpdatePreferencesAndBuildGate(t *testing.T) {
	isolatedGit(t)
	if selfUpdateReleaseContract != "" || selfUpdateReady() {
		t.Fatal("unapproved build enabled")
	}
	if !selfUpdateAutomatic() {
		t.Fatal("default preference is not automatic")
	}
	t.Setenv("BRAIN_NO_AUTO_UPDATE", "1")
	if selfUpdateAutomatic() {
		t.Fatal("environment disable ignored")
	}
	t.Setenv("BRAIN_NO_AUTO_UPDATE", "")
	writeAuthFile("update-config.json", selfUpdateConfig{AutoUpdate: false})
	if selfUpdateAutomatic() {
		t.Fatal("config disable ignored")
	}
	for _, v := range []string{"dev", "1.0.0", "v1.0.0-rc.1", "v1.0.0+local", "v01.0.0", "v1.0.0-dirty", "v1.0", "v18446744073709551616.0.0"} {
		if selfUpdateReleasable(v) {
			t.Errorf("custom version accepted: %s", v)
		}
	}
}

func TestSelfUpdateCheckAuthenticatesVersionWithoutReplacingBinary(t *testing.T) {
	f := newSelfUpdateFixture(t)
	latest, available, err := f.u.run(context.Background(), false, false)
	if err != nil || latest != "v1.1.0" || !available || f.requests != 3 {
		t.Fatalf("check: %q %t %v requests=%d", latest, available, err, f.requests)
	}
	f.unchanged()
	state, err := f.u.loadState()
	if err != nil || state.Highest != "v1.1.0" || state.LastAttempt.IsZero() || state.Installed != "" {
		t.Fatalf("check history: %+v %v", state, err)
	}
	f.publish("v1.0.1")
	if _, _, err := f.u.run(context.Background(), true, false); err == nil {
		t.Fatal("check high-water mark was ignored")
	}
	f.unchanged()
}
func TestSelfUpdateRejectsSymlinkAndWritableDirectories(t *testing.T) {
	t.Run("parent symlink", func(t *testing.T) {
		f := newSelfUpdateFixture(t)
		link := filepath.Join(filepath.Dir(f.u.Exe), "parent-link")
		if err := os.Symlink(filepath.Dir(f.u.Exe), link); err != nil {
			t.Fatal(err)
		}
		f.u.Exe = filepath.Join(link, filepath.Base(f.u.Exe))
		if _, _, err := f.u.run(context.Background(), true, false); err == nil {
			t.Fatal("symlink parent accepted")
		}
	})
	t.Run("writable immediate directory", func(t *testing.T) {
		f := newSelfUpdateFixture(t)
		if err := os.Chmod(filepath.Dir(f.u.Exe), 0777); err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.u.run(context.Background(), true, false); err == nil {
			t.Fatal("shared update directory accepted")
		}
		f.unchanged()
	})
}
func TestSelfUpdateBuildVersionNormalization(t *testing.T) {
	before := version
	t.Cleanup(func() { version = before })
	for _, v := range []string{"0.2.0", "v0.2.0"} {
		version = v
		if selfUpdateBuildVersion() != "v0.2.0" {
			t.Fatalf("stable version not normalized: %q", v)
		}
	}
	for _, v := range []string{"dev", "0.2.0-rc.1", "0.2.0-dirty", "0.2.0+custom"} {
		version = v
		if selfUpdateReleasable(selfUpdateBuildVersion()) {
			t.Fatalf("custom build normalized into stable release: %q", v)
		}
	}
}

func TestSelfUpdateDailyAttemptPolicy(t *testing.T) {
	now := time.Date(2026, 11, 16, 20, 0, 0, 0, time.UTC)
	if !selfUpdateDue(selfUpdateState{}, now) {
		t.Fatal("first attempt disabled")
	}
	for _, age := range []time.Duration{-time.Hour, 0, 23 * time.Hour, 24*time.Hour - time.Second} {
		if selfUpdateDue(selfUpdateState{LastAttempt: now.Add(-age)}, now) {
			t.Fatalf("attempt within daily limit: %s", age)
		}
	}
	if !selfUpdateDue(selfUpdateState{LastAttempt: now.Add(-24 * time.Hour)}, now) {
		t.Fatal("daily attempt not due")
	}
}

func TestSelfUpdateStaleProcessRetainsActualPreviousVersion(t *testing.T) {
	f := newSelfUpdateFixture(t)
	if _, _, err := f.u.run(context.Background(), true, false); err != nil {
		t.Fatal(err)
	}
	first := append([]byte(nil), f.binary...)
	f.binary = []byte("inert second fixture release")
	f.publish("v1.2.0")
	if _, _, err := f.u.run(context.Background(), true, false); err != nil {
		t.Fatal(err)
	}
	state, err := f.u.loadState()
	if err != nil || state.Previous != "v1.1.0" {
		t.Fatalf("incorrect previous version after stale-process update: %+v %v", state, err)
	}
	if err := f.u.rollback(); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(f.u.Exe)
	if string(restored) != string(first) {
		t.Fatal("wrong rollback bytes")
	}
}
