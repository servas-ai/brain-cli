package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func selfUpdateProfile() oidcProfile {
	audience := "397986582964995712"
	return oidcProfile{Issuer: humanOIDCIssuer, ClientID: "397986586068780672", Audience: audience, ForgeURL: selfUpdateOrigin,
		Scopes: []string{"openid", "offline_access", "urn:zitadel:iam:org:project:id:" + audience + ":aud"}}
}

// Deliberately separate from currentAccess: the Forge session, Git credentials,
// PATs and ambient environment never supply an update bearer token.
func currentUpdateAccess(ctx context.Context) (string, error) {
	return selfUpdateAccess(ctx, selfUpdateProfile())
}
func selfUpdateAccess(ctx context.Context, expected oidcProfile) (string, error) {
	var p oidcProfile
	if readAuthFile("update-profile.json", &p) != nil || validateProfile(p) != nil || p.Issuer != expected.Issuer || p.ClientID != expected.ClientID || p.Audience != expected.Audience || p.ForgeURL != expected.ForgeURL {
		return "", errors.New("saved update profile missing or changed; run brain self-update login")
	}
	var s oidcSession
	if readAuthFile("update-session.json", &s) != nil || !matchingSession(p, &s) {
		return "", errors.New("saved update session missing or mismatched; run brain self-update login")
	}
	if time.Until(s.Expiry) < time.Minute {
		next, err := refreshOIDC(ctx, p, &s)
		if err != nil {
			return "", errors.New("update session refresh failed; run brain self-update login")
		}
		if next == nil || !matchingSession(p, next) || next.Subject != s.Subject {
			return "", errors.New("update session identity changed; log in again")
		}
		if err := writeAuthFile("update-session.json", next); err != nil {
			return "", errors.New("cannot save refreshed update session")
		}
		s = *next
	}
	subject, _, err := verifyAccess(ctx, p, s.AccessToken)
	if err != nil || subject != s.Subject {
		return "", errors.New("update access token validation failed; log in again")
	}
	return s.AccessToken, nil
}

type selfUpdateConfig struct {
	AutoUpdate bool `json:"auto_update"`
}

func selfUpdateAutomatic() bool {
	if os.Getenv("BRAIN_NO_AUTO_UPDATE") != "" {
		return false
	}
	var cfg selfUpdateConfig
	d, err := authDir()
	if err != nil {
		return false
	}
	if _, err := os.Lstat(filepath.Join(d, "update-config.json")); errors.Is(err, os.ErrNotExist) {
		return true
	}
	return readAuthFile("update-config.json", &cfg) == nil && cfg.AutoUpdate
}
func newSelfUpdater() (*selfUpdater, error) {
	d, err := authDir()
	if err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, errors.New("cannot locate current binary")
	}
	key, err := base64.StdEncoding.DecodeString(trimUpdateKey())
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, errors.New("invalid embedded release public key")
	}
	return &selfUpdater{BaseURL: selfUpdateOrigin, Current: selfUpdateBuildVersion(), Exe: exe, StatePath: filepath.Join(d, "update-state.json"), Key: key, Token: currentUpdateAccess}, nil
}
func trimUpdateKey() string { return strings.TrimSpace(selfUpdatePublicKey) }
func selfUpdateBuildVersion() string {
	if selfUpdateReleasable(version) {
		return version
	}
	if selfUpdateReleasable("v" + version) {
		return "v" + version
	}
	return version
}
func selfUpdateReady() bool {
	return selfUpdateReleaseContract == "approved" && selfUpdateReleasable(selfUpdateBuildVersion())
}
func selfUpdateError(err error) int { fmt.Fprintln(os.Stderr, "brain self-update:", err); return 1 }
func cmdSelfUpdate(args []string) int {
	action := "install"
	if len(args) > 0 {
		action = args[0]
	}
	if len(args) > 1 {
		fmt.Fprintln(os.Stderr, "Usage: brain self-update [login|status|check|rollback|enable|disable|logout]")
		return 2
	}
	switch action {
	case "login":
		p := selfUpdateProfile()
		if err := validateProfile(p); err != nil {
			return selfUpdateError(err)
		}
		ctx, cancel := authContext()
		defer cancel()
		s, err := loginOIDC(ctx, p, openBrowser)
		if err != nil {
			return selfUpdateError(errors.New("update login failed"))
		}
		if err := writeAuthFile("update-profile.json", p); err != nil {
			return selfUpdateError(err)
		}
		if err := writeAuthFile("update-session.json", s); err != nil {
			return selfUpdateError(err)
		}
		fmt.Println("Update login saved separately from Forge authentication.")
		return 0
	case "logout":
		var s oidcSession
		readErr := readAuthFile("update-session.json", &s)
		ctx, cancel := authContext()
		defer cancel()
		revokeErr := readErr
		if revokeErr == nil && matchingSession(selfUpdateProfile(), &s) {
			revokeErr = revokeOIDC(ctx, selfUpdateProfile(), &s)
		} else {
			revokeErr = errors.New("invalid update session")
		}
		d, err := authDir()
		if err != nil {
			return selfUpdateError(err)
		}
		if err := os.Remove(filepath.Join(d, "update-session.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return selfUpdateError(errors.New("cannot remove local update session"))
		}
		if revokeErr != nil {
			return selfUpdateError(errors.New("local update session removed; server revocation unconfirmed; access tokens remain valid until expiry"))
		}
		fmt.Println("Update session removed; revocation accepted. Existing access tokens expire according to server policy.")
		return 0
	case "enable", "disable":
		if err := writeAuthFile("update-config.json", selfUpdateConfig{AutoUpdate: action == "enable"}); err != nil {
			return selfUpdateError(err)
		}
		fmt.Println("Automatic update preference saved.")
		return 0
	case "status":
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, err := currentUpdateAccess(ctx)
		fmt.Printf("{\"release_contract_ready\":%t,\"auto_update\":%t,\"authenticated\":%t}\n", selfUpdateReady(), selfUpdateAutomatic(), err == nil)
		return 0
	case "install", "check", "rollback", "_background":
	default:
		fmt.Fprintln(os.Stderr, "Usage: brain self-update [login|status|check|rollback|enable|disable|logout]")
		return 2
	}
	background := action == "_background"
	if background && (!selfUpdateReady() || !selfUpdateAutomatic()) {
		return 0
	}
	if !selfUpdateReady() && action != "rollback" {
		return selfUpdateError(errors.New("self-update is gated: approved stable release, signed version binding and Brain clidist registration required"))
	}
	u, err := newSelfUpdater()
	if err != nil {
		if background {
			return 0
		}
		return selfUpdateError(err)
	}
	if action == "rollback" {
		if err := u.rollback(); err != nil {
			return selfUpdateError(err)
		}
		fmt.Println("Previous verified binary restored.")
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	latest, available, err := u.run(ctx, action != "check", background)
	if background {
		return 0
	}
	if err != nil {
		return selfUpdateError(err)
	}
	fmt.Printf("{\"latest\":%q,\"update_available\":%t}\n", latest, available)
	if action == "check" && available {
		return 11
	}
	return 0
}

// Called after an ordinary command. No network or authentication happens in
// the foreground. Child output is discarded and its complete work is bounded.
func selfUpdateDue(state selfUpdateState, now time.Time) bool {
	return state.LastAttempt.IsZero() || now.Sub(state.LastAttempt) >= 24*time.Hour
}
func maybeBackgroundUpdate() {
	if !selfUpdateReady() || !selfUpdateAutomatic() {
		return
	}
	u, err := newSelfUpdater()
	if err != nil {
		return
	}
	unlock, err := u.lock()
	if err != nil {
		return
	}
	state, err := u.loadState()
	if err != nil || !selfUpdateDue(state, time.Now()) {
		unlock()
		return
	}
	state.LastAttempt = time.Now().UTC()
	err = u.saveState(state)
	unlock()
	if err == nil {
		_ = selfUpdateSpawn(u.Exe)
	}
}
