package main

// Self-update is an independent standard-library implementation of the clidist
// wire protocol inspected in servas-ai/vmcli commit
// 969a93405780772600d376e7318d26a754815091, internal/selfupdate, and clidist
// internal/store/verify.go blob 4f4b325348a45b9b65cec2ad2deec01dce3a988d.
// No source was copied: neither repository supplies a verifiable license file
// in the inspected source. No x/mod or other dependency was added.
//
// RELEASE CONTRACT (not activated by this source change): SHA256SUMS must list
// brain-<os>-<arch> AND brain-version-<vX.Y.Z>, whose hash is SHA256(vX.Y.Z+"\n").
// Sign the exact SHA256SUMS bytes with the approved Ed25519 release key. The
// marker binds unsigned latest.version to the signed binary checksum. clidist's
// inspected parser accepts the marker; comments are not accepted. Publishing
// the marker file is recommended, although the server permits manifest-only
// entries. Unversioned legacy manifests fail closed. Brain registration,
// authorized release signing/publication and live acceptance remain gates.

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const selfUpdateOrigin = "https://cli.servas.ai"
const selfUpdateMaxBinary = 128 << 20

// This is deliberately not enabled in unsigned or merely tagged builds. Only
// an approved release pipeline may set this to "approved" after the gates above.
var selfUpdateReleaseContract string

// Public key only, originating in vmcli at the commit documented above.
//
//go:embed selfupdate-release-key.pub
var selfUpdatePublicKey string

var selfUpdateVersionRE = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
var selfUpdateNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func selfUpdateVersion(v string) ([3]uint64, bool) {
	var out [3]uint64
	m := selfUpdateVersionRE.FindStringSubmatch(v)
	if m == nil {
		return out, false
	}
	for i := range out {
		n, err := strconv.ParseUint(m[i+1], 10, 64)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
func selfUpdateCompare(a, b string) int {
	x, ax := selfUpdateVersion(a)
	y, by := selfUpdateVersion(b)
	if !ax || !by {
		return 0
	}
	for i := range x {
		if x[i] < y[i] {
			return -1
		}
		if x[i] > y[i] {
			return 1
		}
	}
	return 0
}
func selfUpdateReleasable(v string) bool { _, ok := selfUpdateVersion(v); return ok }
func selfUpdateHash(b []byte) string     { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

type selfUpdateRelease struct {
	CLI     string `json:"cli"`
	Version string `json:"version"`
	File    struct {
		Name   string `json:"name"`
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
		URL    string `json:"url"`
	} `json:"file"`
	SumsURL      string `json:"sha256sums_url"`
	SignatureURL string `json:"signature_url"`
}
type selfUpdateState struct {
	LastAttempt   time.Time `json:"last_attempt,omitempty"`
	Highest       string    `json:"highest_verified_version,omitempty"`
	Installed     string    `json:"installed_version,omitempty"`
	InstalledHash string    `json:"installed_sha256,omitempty"`
	Previous      string    `json:"previous_version,omitempty"`
	PreviousHash  string    `json:"previous_sha256,omitempty"`
	Path          string    `json:"binary_path,omitempty"`
	Skip          string    `json:"rolled_back_version,omitempty"`
}
type selfUpdater struct {
	BaseURL, Current, Exe, StatePath string
	Key                              ed25519.PublicKey
	Token                            func(context.Context) (string, error)
	HTTP                             *http.Client // only package tests inject a transport trusting their own TLS CA
}

func (u *selfUpdater) get(ctx context.Context, path string, limit int64) ([]byte, error) {
	base, err := url.Parse(u.BaseURL)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil || base.Path != "" || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("invalid update origin")
	}
	ref, err := url.Parse(path)
	if err != nil || !strings.HasPrefix(path, "/v1/brain/") || ref.IsAbs() || ref.Host != "" || ref.User != nil || ref.Fragment != "" || ref.RawPath != "" {
		return nil, errors.New("invalid update path")
	}
	if u.Token == nil {
		return nil, errors.New("saved update login required")
	}
	token, err := u.Token(ctx)
	if err != nil || token == "" {
		return nil, errors.New("update authentication failed; run brain self-update login")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.ResolveReference(ref).String(), nil)
	if err != nil {
		return nil, errors.New("cannot create update request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "brain/"+u.Current)
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("update redirects are forbidden") }}
	if u.HTTP != nil {
		client.Transport = u.HTTP.Transport
	}
	if transport, ok := client.Transport.(*http.Transport); ok && transport.TLSClientConfig != nil && transport.TLSClientConfig.InsecureSkipVerify {
		return nil, errors.New("unverified update TLS is forbidden")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("update request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update service returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errors.New("update download incomplete or too large")
	}
	return b, nil
}

func selfUpdateSums(key ed25519.PublicKey, sums, signature []byte, r *selfUpdateRelease) error {
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signature)))
	if err != nil || len(key) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize || !ed25519.Verify(key, sums, sig) {
		return errors.New("release signature verification failed")
	}
	entries := map[string]string{}
	markers := 0
	for _, line := range strings.Split(string(sums), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) < 67 || line[64] != ' ' || (line[65] != ' ' && line[65] != '*') {
			return errors.New("invalid signed checksum manifest")
		}
		name := line[66:]
		hash := strings.ToLower(line[:64])
		raw, err := hex.DecodeString(hash)
		if err != nil || len(raw) != 32 || !selfUpdateNameRE.MatchString(name) || name == "SHA256SUMS" || name == "SHA256SUMS.sig" || entries[name] != "" {
			return errors.New("invalid signed checksum entry")
		}
		entries[name] = hash
		if strings.HasPrefix(name, "brain-version-") {
			markers++
		}
	}
	if markers != 1 || entries["brain-version-"+r.Version] != selfUpdateHash([]byte(r.Version+"\n")) {
		return errors.New("signed release version binding missing or mismatched")
	}
	if entries[r.File.Name] != r.File.SHA256 {
		return errors.New("signed release checksum mismatch")
	}
	return nil
}

func (u *selfUpdater) release(ctx context.Context) (*selfUpdateRelease, error) {
	if !selfUpdateReleasable(u.Current) {
		return nil, errors.New("self-update disabled for development, prerelease or custom builds")
	}
	if len(u.Key) != ed25519.PublicKeySize {
		return nil, errors.New("approved release key missing")
	}
	b, err := u.get(ctx, "/v1/brain/latest?os="+runtime.GOOS+"&arch="+runtime.GOARCH, 1<<20)
	if err != nil {
		return nil, err
	}
	var r selfUpdateRelease
	if json.Unmarshal(b, &r) != nil || r.CLI != "brain" || !selfUpdateReleasable(r.Version) {
		return nil, errors.New("invalid update release metadata")
	}
	name := "brain-" + runtime.GOOS + "-" + runtime.GOARCH
	prefix := "/v1/brain/" + r.Version + "/"
	if r.File.Name != name || r.File.URL != prefix+name || r.SumsURL != prefix+"SHA256SUMS" || r.SignatureURL != prefix+"SHA256SUMS.sig" || r.File.Size < 1 || r.File.Size > selfUpdateMaxBinary {
		return nil, errors.New("update release paths or size do not match the requested version and platform")
	}
	hash, err := hex.DecodeString(r.File.SHA256)
	if err != nil || len(hash) != 32 || strings.ToLower(r.File.SHA256) != r.File.SHA256 {
		return nil, errors.New("invalid release checksum")
	}
	sums, err := u.get(ctx, r.SumsURL, 1<<20)
	if err != nil {
		return nil, err
	}
	sig, err := u.get(ctx, r.SignatureURL, 4096)
	if err != nil {
		return nil, err
	}
	if err := selfUpdateSums(u.Key, sums, sig, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Path components must not be symlinks. A sticky shared ancestor such as /tmp
// is allowed, but the immediate binary/state directory must be private or 0755.
func selfUpdateSafeParents(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("unsafe update path")
	}
	var dirs []os.FileInfo
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		st, err := os.Lstat(dir)
		if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe update directory")
		}
		dirs = append(dirs, st)
		if filepath.Dir(dir) == dir {
			break
		}
	}
	private := false
	for i := len(dirs) - 1; i >= 0; i-- {
		st := dirs[i]
		// An owned 0700 ancestor denies other users traversal into descendants.
		// A writable ancestor above that boundary is still rejected.
		if st.Mode().Perm()&0022 != 0 && (i == 0 || (!private && st.Mode()&os.ModeSticky == 0)) {
			return errors.New("update directory is writable by other users")
		}
		if st.Mode().Perm()&0077 == 0 && selfUpdatePrivateDir(st) {
			private = true
		}
	}
	return nil
}
func selfUpdateOpen(path string) (*os.File, error) {
	if err := selfUpdateSafeParents(path); err != nil {
		return nil, err
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || before.Mode().Perm()&0022 != 0 || !selfUpdateOwned(before) {
		return nil, errors.New("unsafe update file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot read update file")
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		f.Close()
		return nil, errors.New("update file changed while opening")
	}
	return f, nil
}
func selfUpdateFileHash(path string) (string, error) {
	f, err := selfUpdateOpen(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, selfUpdateMaxBinary+1))
	if err != nil || n > selfUpdateMaxBinary {
		return "", errors.New("cannot hash update file")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (u *selfUpdater) loadState() (selfUpdateState, error) {
	var state selfUpdateState
	if err := selfUpdateSafeParents(u.StatePath); err != nil {
		return state, err
	}
	if _, err := os.Lstat(u.StatePath); errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	f, err := selfUpdateOpen(u.StatePath)
	if err != nil {
		return state, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Mode().Perm()&0077 != 0 {
		return state, errors.New("update state must be private")
	}
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(b) > 1<<20 || json.Unmarshal(b, &state) != nil {
		return state, errors.New("invalid update state")
	}
	if state.Highest != "" && !selfUpdateReleasable(state.Highest) {
		return state, errors.New("invalid update history")
	}
	return state, nil
}
func selfUpdateWrite(path string, data []byte, mode os.FileMode) error {
	if err := selfUpdateSafeParents(path); err != nil {
		return err
	}
	if st, err := os.Lstat(path); err == nil {
		if !st.Mode().IsRegular() || !selfUpdateOwned(st) || st.Mode().Perm()&0022 != 0 {
			return errors.New("unsafe update destination")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect update destination")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".brain-update-*")
	if err != nil {
		return errors.New("cannot stage update file")
	}
	defer os.Remove(f.Name())
	err = f.Chmod(mode)
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		return errors.New("cannot commit update file")
	}
	d, err := os.Open(filepath.Dir(path))
	if err == nil {
		err = d.Sync()
		d.Close()
	}
	if err != nil {
		return errors.New("cannot sync update directory")
	}
	return nil
}
func (u *selfUpdater) saveState(s selfUpdateState) error {
	b, err := json.Marshal(s)
	if err != nil {
		return errors.New("cannot encode update state")
	}
	return selfUpdateWrite(u.StatePath, b, 0600)
}
func (u *selfUpdater) lock() (func(), error) {
	if err := selfUpdateSafeParents(u.StatePath); err != nil {
		return nil, err
	}
	p := u.StatePath + ".lock"
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, errors.New("another update is running or update lock needs inspection")
	}
	f.Close()
	return func() { _ = os.Remove(p) }, nil
}

// run verifies signatures even for a check, so only authenticated signed
// versions advance the persisted high-water mark. A failed download never
// touches the existing binary or its rollback copy.
func (u *selfUpdater) run(ctx context.Context, install, automatic bool) (string, bool, error) {
	unlock, err := u.lock()
	if err != nil {
		return "", false, err
	}
	defer unlock()
	state, err := u.loadState()
	if err != nil {
		return "", false, err
	}
	state.LastAttempt = time.Now().UTC()
	if err := u.saveState(state); err != nil {
		return "", false, err
	}
	r, err := u.release(ctx)
	if err != nil {
		return "", false, err
	}
	if selfUpdateCompare(r.Version, u.Current) < 0 || (state.Highest != "" && selfUpdateCompare(r.Version, state.Highest) < 0) {
		return "", false, errors.New("release replay or downgrade rejected")
	}
	if state.Highest == "" || selfUpdateCompare(r.Version, state.Highest) > 0 {
		state.Highest = r.Version
	}
	if err := u.saveState(state); err != nil {
		return "", false, err
	}
	available := selfUpdateCompare(r.Version, u.Current) > 0
	if !install || !available || (automatic && state.Skip == r.Version) {
		return r.Version, available, nil
	}
	if err := selfUpdatePlatform(); err != nil {
		return "", false, err
	}
	oldHash, err := selfUpdateFileHash(u.Exe)
	if err != nil {
		return "", false, err
	}
	installedFrom := u.Current
	if state.Path == u.Exe && state.InstalledHash != "" {
		if !selfUpdateReleasable(state.Installed) {
			return "", false, errors.New("invalid installed version history")
		}
		installedFrom = state.Installed
		if oldHash != state.InstalledHash {
			return "", false, errors.New("installed binary does not match update history")
		}
		if selfUpdateCompare(state.Installed, r.Version) >= 0 {
			return r.Version, false, nil
		}
	}
	if _, err := os.Lstat(u.Exe + ".prev"); err == nil {
		previousHash, err := selfUpdateFileHash(u.Exe + ".prev")
		if err != nil || state.Path != u.Exe || state.PreviousHash == "" || previousHash != state.PreviousHash {
			return "", false, errors.New("existing rollback binary is not trusted")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, errors.New("cannot inspect rollback binary")
	}
	data, err := u.get(ctx, r.File.URL, selfUpdateMaxBinary)
	if err != nil {
		return "", false, err
	}
	if int64(len(data)) != r.File.Size || selfUpdateHash(data) != r.File.SHA256 {
		return "", false, errors.New("downloaded binary checksum or size mismatch")
	}
	// Snapshot into an independent file: no hardlink can mutate the rollback
	// through the old inode. Recheck the current bytes before committing.
	old, err := selfUpdateOpen(u.Exe)
	if err != nil {
		return "", false, err
	}
	oldBytes, readErr := io.ReadAll(io.LimitReader(old, selfUpdateMaxBinary+1))
	old.Close()
	if readErr != nil || selfUpdateHash(oldBytes) != oldHash {
		return "", false, errors.New("installed binary changed during update")
	}
	if err := selfUpdateWrite(u.Exe+".prev", oldBytes, 0755); err != nil {
		return "", false, err
	}
	state.Path, state.Previous, state.PreviousHash = u.Exe, installedFrom, oldHash
	state.Installed, state.InstalledHash, state.Skip = r.Version, r.File.SHA256, ""
	// Journal first. A crash before the binary rename leaves a safe mismatch
	// and rejects further automatic replacement until an operator inspects it.
	if err := u.saveState(state); err != nil {
		return "", false, err
	}
	if err := selfUpdateWrite(u.Exe, data, 0755); err != nil {
		return "", false, err
	}
	return r.Version, true, nil
}
func (u *selfUpdater) rollback() error {
	if err := selfUpdatePlatform(); err != nil {
		return err
	}
	unlock, err := u.lock()
	if err != nil {
		return err
	}
	defer unlock()
	state, err := u.loadState()
	if err != nil {
		return err
	}
	if state.Path != u.Exe || state.PreviousHash == "" || !selfUpdateReleasable(state.Previous) {
		return errors.New("no verified rollback is available")
	}
	current, err := selfUpdateFileHash(u.Exe)
	if err != nil || current != state.InstalledHash {
		return errors.New("installed binary changed; rollback rejected")
	}
	previous, err := selfUpdateFileHash(u.Exe + ".prev")
	if err != nil || previous != state.PreviousHash {
		return errors.New("rollback binary changed; rollback rejected")
	}
	f, err := selfUpdateOpen(u.Exe + ".prev")
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(f, selfUpdateMaxBinary+1))
	f.Close()
	if err != nil || selfUpdateHash(data) != state.PreviousHash {
		return errors.New("rollback verification failed")
	}
	state.Skip, state.Installed, state.InstalledHash = state.Installed, state.Previous, state.PreviousHash
	state.Previous = ""
	if err := u.saveState(state); err != nil {
		return err
	}
	if err := selfUpdateWrite(u.Exe, data, 0755); err != nil {
		return err
	}
	// The previous copy remains as a verified recovery artifact; a second
	// rollback is denied because the journal has consumed its authorization.
	return nil
}
