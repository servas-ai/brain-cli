package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// All identities, keys, codes, and OAuth responses below exist exclusively in
// this process. No test contacts the real issuer, Forge, or an existing account.
type oidcFixture struct {
	t                                           *testing.T
	server                                      *httptest.Server
	key                                         *rsa.PrivateKey
	profile                                     oidcProfile
	ctx                                         context.Context
	mu                                          sync.Mutex
	metadata                                    map[string]any
	nonce, challenge, redirect                  string
	accessIssuer, accessAudience, accessSubject string
	idNonce                                     string
	expired                                     bool
	tokenError                                  bool
	tokenRedirect                               bool
	discoveryRedirect                           bool
	jwksOverride                                any
	refreshes, exchanges, leaks                 int
	revoked                                     []string
}

func newOIDCFixture(t *testing.T) *oidcFixture {
	t.Helper()
	previous := testIssuerAllowed
	testIssuerAllowed = true
	t.Cleanup(func() { testIssuerAllowed = previous })
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &oidcFixture{t: t, key: key, accessSubject: "tls-fixture-subject"}
	f.server = httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
	f.server.Config.ErrorLog = log.New(io.Discard, "", 0)
	f.server.StartTLS()
	t.Cleanup(f.server.Close)
	issuer := f.server.URL
	f.profile = oidcProfile{Issuer: issuer, ClientID: "fixture-public-native-client", Audience: "fixture-forge-api", ForgeURL: issuer, Scopes: []string{"openid", "offline_access", "urn:zitadel:iam:org:project:id:fixture-forge-api:aud"}}
	f.accessIssuer = issuer
	f.accessAudience = f.profile.Audience
	f.ctx = context.WithValue(context.Background(), oidcHTTPClientKey{}, f.server.Client())
	f.metadata = map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks", "revocation_endpoint": issuer + "/revoke", "code_challenge_methods_supported": []string{"S256"}, "id_token_signing_alg_values_supported": []string{"RS256"}, "authorization_response_iss_parameter_supported": true}
	return f
}

func (f *oidcFixture) sign(issuer, audience, subject, nonce string, expired bool) string {
	now := time.Now()
	expiry := now.Add(time.Hour)
	if expired {
		expiry = now.Add(-time.Minute)
	}
	claims := oidcClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: issuer, Audience: jwt.ClaimStrings{audience}, Subject: subject, ExpiresAt: jwt.NewNumericDate(expiry), IssuedAt: jwt.NewNumericDate(now.Add(-2 * time.Minute))}, Nonce: nonce, AuthorizedParty: f.profile.ClientID}
	if audience == f.profile.Audience {
		claims.ID = "tls-fixture-access-jti"
		claims.NotBefore = jwt.NewNumericDate(now.Add(-2 * time.Minute))
	}
	return f.signClaims(claims)
}

func (f *oidcFixture) signClaims(claims oidcClaims) string {
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "fixture-key"
	raw, err := token.SignedString(f.key)
	if err != nil {
		f.t.Fatal(err)
	}
	return raw
}

func (f *oidcFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		if f.discoveryRedirect {
			http.Redirect(w, r, "/leak", http.StatusTemporaryRedirect)
			return
		}
		_ = json.NewEncoder(w).Encode(f.metadata)
	case "/jwks":
		if f.jwksOverride != nil {
			_ = json.NewEncoder(w).Encode(f.jwksOverride)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "fixture-key", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(f.key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(f.key.E)).Bytes())}}})
	case "/token":
		if f.tokenRedirect {
			http.Redirect(w, r, "/leak", http.StatusTemporaryRedirect)
			return
		}
		if f.tokenError {
			http.Error(w, "do not leak fixture-refresh-secret authorization-code-secret", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodPost {
			f.t.Error("token method")
		}
		if r.Header.Get("Authorization") != "" {
			f.t.Error("public client sent authorization")
		}
		if err := r.ParseForm(); err != nil {
			f.t.Error(err)
			return
		}
		if r.Form.Get("client_secret") != "" || r.Form.Get("client_id") != f.profile.ClientID {
			f.t.Error("unexpected native client credentials")
		}
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			f.exchanges++
			challenge := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(challenge[:]) != f.challenge || r.Form.Get("redirect_uri") != f.redirect || r.Form.Get("code") != "authorization-code-secret" {
				f.t.Error("PKCE/code/redirect mismatch")
			}
		case "refresh_token":
			f.refreshes++
			if r.Form.Get("refresh_token") != "fixture-refresh-secret" {
				f.t.Error("unexpected refresh token")
			}
		default:
			f.t.Error("unexpected grant")
		}
		nonce := f.nonce
		if f.idNonce != "" {
			nonce = f.idNonce
		}
		_ = json.NewEncoder(w).Encode(oidcTokenResponse{TokenType: "Bearer", AccessToken: f.sign(f.accessIssuer, f.accessAudience, f.accessSubject, "", f.expired), IDToken: f.sign(f.profile.Issuer, f.profile.ClientID, f.accessSubject, nonce, false), RefreshToken: "fixture-refresh-secret"})
	case "/revoke":
		if r.Method != http.MethodPost {
			f.t.Error("revocation method")
		}
		_ = r.ParseForm()
		if r.Form.Get("client_id") != f.profile.ClientID || r.Form.Get("client_secret") != "" || r.Header.Get("Authorization") != "" {
			f.t.Error("revocation credentials")
		}
		f.revoked = append(f.revoked, r.Form.Get("token_type_hint"))
		w.WriteHeader(http.StatusOK)
	case "/leak":
		f.leaks++
		w.WriteHeader(http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

func (f *oidcFixture) browser(change func(url.Values)) func(string) {
	return func(raw string) {
		u, err := url.Parse(raw)
		if err != nil {
			f.t.Fatal(err)
		}
		q := u.Query()
		f.mu.Lock()
		f.nonce = q.Get("nonce")
		f.challenge = q.Get("code_challenge")
		f.redirect = q.Get("redirect_uri")
		f.mu.Unlock()
		callback, err := url.Parse(q.Get("redirect_uri"))
		if err != nil {
			f.t.Fatal(err)
		}
		if callback.Scheme != "http" || callback.Hostname() != "127.0.0.1" || callback.Port() == "" || callback.Path != "/callback" {
			f.t.Fatalf("unsafe redirect: %s", callback.Hostname())
		}
		if len(q.Get("state")) < 40 || len(q.Get("nonce")) < 40 || len(q.Get("code_challenge")) < 40 || q.Get("code_challenge_method") != "S256" || q.Get("response_type") != "code" || q.Get("audience") != f.profile.Audience || q.Get("client_id") != f.profile.ClientID {
			f.t.Fatal("missing PKCE/native OIDC parameters")
		}
		values := url.Values{"state": {q.Get("state")}, "code": {"authorization-code-secret"}, "iss": {f.profile.Issuer}}
		if change != nil {
			change(values)
		}
		callback.RawQuery = values.Encode()
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get(callback.String())
		if err != nil {
			f.t.Fatal("loopback callback failed")
		}
		resp.Body.Close()
	}
}

func TestOIDCLoginRefreshRevokeTLS(t *testing.T) {
	f := newOIDCFixture(t)
	session, err := loginOIDC(f.ctx, f.profile, f.browser(nil))
	if err != nil {
		t.Fatal(err)
	}
	if session.Subject != "tls-fixture-subject" || session.AccessToken == "" || session.IDToken == "" || session.RefreshToken == "" || !session.Expiry.After(time.Now()) {
		t.Fatal("incomplete verified session")
	}
	refreshed, err := refreshOIDC(f.ctx, f.profile, session)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Subject != session.Subject || f.refreshes != 1 || f.exchanges != 1 {
		t.Fatal("refresh/exchange mismatch")
	}
	if err := revokeOIDC(f.ctx, f.profile, refreshed); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(f.revoked) != "[refresh_token access_token]" {
		t.Fatal("both credentials must be revoked")
	}
}

func TestOIDCCallbackRejects(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(url.Values)
	}{
		{"state", func(q url.Values) { q.Set("state", "wrong-state") }},
		{"issuer", func(q url.Values) { q.Set("iss", "https://wrong-issuer.invalid") }},
		{"missing issuer", func(q url.Values) { q.Del("iss") }},
		{"duplicate state", func(q url.Values) { q.Add("state", "attacker-state") }},
		{"oauth error", func(q url.Values) {
			q.Set("error", "secret-provider-error")
			q.Set("error_description", "authorization-code-secret")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOIDCFixture(t)
			session, err := loginOIDC(f.ctx, f.profile, f.browser(tc.change))
			if err == nil || session != nil || f.exchanges != 0 {
				t.Fatal("invalid callback reached token exchange")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("callback error leaked credentials")
			}
		})
	}
}

func TestOIDCJWTValidation(t *testing.T) {
	f := newOIDCFixture(t)
	for _, tc := range []struct {
		name, issuer, audience, subject string
		expired                         bool
	}{
		{"valid", f.profile.Issuer, f.profile.Audience, "fixture", false},
		{"wrong issuer", "https://wrong-issuer.invalid", f.profile.Audience, "fixture", false},
		{"wrong audience", f.profile.Issuer, "wrong-api", "fixture", false},
		{"empty subject", f.profile.Issuer, f.profile.Audience, "", false},
		{"expired", f.profile.Issuer, f.profile.Audience, "fixture", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := f.sign(tc.issuer, tc.audience, tc.subject, "", tc.expired)
			sub, expiry, err := verifyAccess(f.ctx, f.profile, raw)
			if tc.name == "valid" {
				if err != nil || sub != "fixture" || !expiry.After(time.Now()) {
					t.Fatal("valid token rejected", err)
				}
			} else if err == nil {
				t.Fatal("invalid claims accepted")
			}
		})
	}
	raw := f.sign(f.profile.Issuer, f.profile.Audience, "fixture", "", false)
	parts := strings.Split(raw, ".")
	parts[2] = base64.RawURLEncoding.EncodeToString(make([]byte, 256))
	if _, _, err := verifyAccess(f.ctx, f.profile, strings.Join(parts, ".")); err == nil {
		t.Fatal("invalid signature accepted")
	}
	hs := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"iss": f.profile.Issuer, "aud": f.profile.Audience, "sub": "fixture", "exp": time.Now().Add(time.Hour).Unix()})
	hs.Header["kid"] = "fixture-key"
	forged, _ := hs.SignedString(f.key.N.Bytes())
	if _, _, err := verifyAccess(f.ctx, f.profile, forged); err == nil {
		t.Fatal("algorithm substitution accepted")
	}
}

func TestOIDCLoginRejectsNonceAndAccessAudience(t *testing.T) {
	for _, mode := range []string{"nonce", "audience", "expired"} {
		t.Run(mode, func(t *testing.T) {
			f := newOIDCFixture(t)
			switch mode {
			case "nonce":
				f.idNonce = "wrong-nonce"
			case "audience":
				f.accessAudience = "wrong-api"
			case "expired":
				f.expired = true
			}
			if session, err := loginOIDC(f.ctx, f.profile, f.browser(nil)); err == nil || session != nil {
				t.Fatal("invalid token accepted on login")
			}
		})
	}
}

func TestOIDCRefreshRejectsChangedIdentityExpiredAndProfile(t *testing.T) {
	for _, mode := range []string{"subject", "expired", "profile", "audience"} {
		t.Run(mode, func(t *testing.T) {
			f := newOIDCFixture(t)
			s := &oidcSession{Issuer: f.profile.Issuer, ClientID: f.profile.ClientID, Audience: f.profile.Audience, ForgeURL: f.profile.ForgeURL, Subject: "tls-fixture-subject", RefreshToken: "fixture-refresh-secret"}
			switch mode {
			case "subject":
				f.accessSubject = "another-subject"
			case "expired":
				f.expired = true
			case "profile":
				s.ClientID = "other-client"
			case "audience":
				f.accessAudience = "other-api"
			}
			if result, err := refreshOIDC(f.ctx, f.profile, s); err == nil || result != nil {
				t.Fatal("unsafe refresh accepted")
			}
			if mode == "profile" && f.refreshes != 0 {
				t.Fatal("mismatched profile sent credential")
			}
		})
	}
}

func TestOIDCDiscoveryBoundaries(t *testing.T) {
	for _, mode := range []string{"issuer", "http", "origin", "revoke origin", "pkce", "algorithm", "untrusted TLS"} {
		t.Run(mode, func(t *testing.T) {
			f := newOIDCFixture(t)
			switch mode {
			case "issuer":
				f.metadata["issuer"] = "https://other.invalid"
			case "http":
				f.metadata["token_endpoint"] = "http://127.0.0.1/token"
			case "origin":
				f.metadata["jwks_uri"] = "https://other.invalid/jwks"
			case "revoke origin":
				f.metadata["revocation_endpoint"] = "https://other.invalid/revoke"
			case "pkce":
				f.metadata["code_challenge_methods_supported"] = []string{"plain"}
			case "algorithm":
				f.metadata["id_token_signing_alg_values_supported"] = []string{"HS256"}
			case "untrusted TLS":
				f.ctx = context.Background()
			}
			opened := false
			if _, err := loginOIDC(f.ctx, f.profile, func(string) { opened = true }); err == nil {
				t.Fatal("unsafe discovery accepted")
			}
			if opened {
				t.Fatal("browser opened before discovery was validated")
			}
		})
	}
}

func TestOIDCTokenErrorsAreRedactedAndRedirectsRejected(t *testing.T) {
	for _, redirect := range []bool{false, true} {
		t.Run(fmt.Sprint(redirect), func(t *testing.T) {
			f := newOIDCFixture(t)
			f.tokenRedirect = redirect
			f.tokenError = !redirect
			s, err := loginOIDC(f.ctx, f.profile, f.browser(nil))
			if s != nil || err == nil {
				t.Fatal("token failure accepted")
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), f.server.URL) {
				t.Fatal("remote error leaked")
			}
			if f.leaks != 0 {
				t.Fatal("redirect followed")
			}
		})
	}
}

func TestOIDCRevocationUnsupported(t *testing.T) {
	f := newOIDCFixture(t)
	delete(f.metadata, "revocation_endpoint")
	s := &oidcSession{Issuer: f.profile.Issuer, ClientID: f.profile.ClientID, Audience: f.profile.Audience, ForgeURL: f.profile.ForgeURL, Subject: "fixture", RefreshToken: "fixture-refresh-secret"}
	if err := revokeOIDC(f.ctx, f.profile, s); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatal("unsupported revocation not reported")
	}
}

func TestOIDCProductionIssuerPinAndCancellation(t *testing.T) {
	f := newOIDCFixture(t)
	testIssuerAllowed = false
	if err := validateProfile(f.profile); err == nil {
		t.Fatal("production accepted test issuer")
	}
	p := f.profile
	p.Issuer = humanOIDCIssuer
	if err := validateProfile(p); err != nil {
		t.Fatal(err)
	}
	testIssuerAllowed = true
	ctx, cancel := context.WithCancel(f.ctx)
	s, err := loginOIDC(ctx, f.profile, func(string) { cancel() })
	if err == nil || s != nil {
		t.Fatal("cancelled login succeeded")
	}
}

func TestOIDCIDTokenBinding(t *testing.T) {
	for _, mode := range []string{"valid hash", "wrong hash", "wrong client", "wrong subject", "multiple audience without azp"} {
		t.Run(mode, func(t *testing.T) {
			f := newOIDCFixture(t)
			access := f.sign(f.profile.Issuer, f.profile.Audience, "fixture", "", false)
			digest := sha256.Sum256([]byte(access))
			claims := oidcClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: f.profile.Issuer, Audience: jwt.ClaimStrings{f.profile.ClientID}, Subject: "fixture", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}, Nonce: "fixture-nonce", AccessHash: base64.RawURLEncoding.EncodeToString(digest[:16])}
			switch mode {
			case "wrong hash":
				claims.AccessHash = "invalid"
			case "wrong client":
				claims.Audience = jwt.ClaimStrings{"other-client"}
			case "wrong subject":
				claims.Subject = "another-fixture"
			case "multiple audience without azp":
				claims.Audience = append(claims.Audience, "other-client")
			}
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
			token.Header["kid"] = "fixture-key"
			id, err := token.SignedString(f.key)
			if err != nil {
				t.Fatal(err)
			}
			provider, err := discoverOIDC(f.ctx, f.profile)
			if err != nil {
				t.Fatal(err)
			}
			result, err := provider.session(f.ctx, &oidcTokenResponse{AccessToken: access, IDToken: id}, "fixture-nonce", "", true)
			if mode == "valid hash" {
				if err != nil || result == nil {
					t.Fatal("valid at_hash failed", err)
				}
			} else if err == nil || result != nil {
				t.Fatal("invalid ID binding accepted")
			}
		})
	}
}

func TestOIDCJWKSRejectsAmbiguousOrSmallKeys(t *testing.T) {
	for _, mode := range []string{"duplicate kid", "small key", "wrong use", "wrong algorithm", "wrong key ops"} {
		t.Run(mode, func(t *testing.T) {
			f := newOIDCFixture(t)
			k := map[string]any{"kty": "RSA", "kid": "fixture-key", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(f.key.N.Bytes()), "e": "AQAB"}
			keys := []map[string]any{k}
			switch mode {
			case "duplicate kid":
				keys = append(keys, k)
			case "small key":
				k["n"] = "AQAB"
			case "wrong use":
				k["use"] = "enc"
			case "wrong algorithm":
				k["alg"] = "HS256"
			case "wrong key ops":
				k["key_ops"] = []string{"encrypt"}
			}
			f.jwksOverride = map[string]any{"keys": keys}
			if _, _, err := verifyAccess(f.ctx, f.profile, f.sign(f.profile.Issuer, f.profile.Audience, "fixture", "", false)); err == nil {
				t.Fatal("unsafe JWKS accepted")
			}
		})
	}
}

func TestOIDCDiscoveryRedirectRejected(t *testing.T) {
	f := newOIDCFixture(t)
	f.discoveryRedirect = true
	if _, err := discoverOIDC(f.ctx, f.profile); err == nil {
		t.Fatal("discovery redirect accepted")
	}
	if f.leaks != 0 {
		t.Fatal("discovery redirect followed")
	}
}

func TestOIDCProfileValidation(t *testing.T) {
	f := newOIDCFixture(t)
	for _, mode := range []string{"forge path", "forge slash", "missing scope", "management audience", "client whitespace", "audience control", "scope control", "same client and audience"} {
		t.Run(mode, func(t *testing.T) {
			p := f.profile
			p.Scopes = append([]string(nil), p.Scopes...)
			switch mode {
			case "forge path":
				p.ForgeURL += "/forge"
			case "forge slash":
				p.ForgeURL += "/"
			case "missing scope":
				p.Scopes = []string{"openid", "offline_access"}
			case "management audience":
				p.Audience = "zitadel"
			case "client whitespace":
				p.ClientID = "bad client"
			case "audience control":
				p.Audience = "bad\x01"
			case "scope control":
				p.Scopes = append(p.Scopes, "bad\x01")
			case "same client and audience":
				p.ClientID = p.Audience
			}
			if err := validateProfile(p); err == nil {
				t.Fatal("invalid profile accepted")
			}
		})
	}
}

// ID-token substitution must fail even when ZITADEL includes the requested
// project audience and the correct azp on a correctly signed, live ID token.
func TestOIDCRejectsProjectAudienceIDTokenAsAccess(t *testing.T) {
	f := newOIDCFixture(t)
	now := time.Now()
	for _, nonce := range []string{"fixture-id-nonce", ""} {
		t.Run("nonce="+nonce, func(t *testing.T) {
			idClaims := oidcClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: f.profile.Issuer, Subject: "fixture", Audience: jwt.ClaimStrings{f.profile.ClientID, f.profile.Audience}, IssuedAt: jwt.NewNumericDate(now.Add(-time.Minute)), ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour))}, AuthorizedParty: f.profile.ClientID, Nonce: nonce}
			raw := f.signClaims(idClaims)
			if _, _, err := verifyAccess(f.ctx, f.profile, raw); err == nil {
				t.Fatal("ID token with project audience accepted as access token")
			}
			provider, err := discoverOIDC(f.ctx, f.profile)
			if err != nil {
				t.Fatal(err)
			}
			if s, err := provider.session(f.ctx, &oidcTokenResponse{AccessToken: raw}, "", "fixture", false); err == nil || s != nil {
				t.Fatal("refresh/session accepted ID token as access token")
			}
			// It remains a valid ID token with a multi-valued audience and exact azp.
			access := f.sign(f.profile.Issuer, f.profile.Audience, "fixture", "", false)
			if _, err := provider.session(f.ctx, &oidcTokenResponse{AccessToken: access, IDToken: raw}, nonce, "fixture", true); err != nil {
				t.Fatal("normal ID-token validation changed", err)
			}
		})
	}
}

func TestOIDCAccessClaimShape(t *testing.T) {
	f := newOIDCFixture(t)
	provider, err := discoverOIDC(f.ctx, f.profile)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"valid", "missing azp", "wrong azp", "missing jti", "blank jti", "missing nbf", "future nbf", "missing iat", "future iat", "nonce", "at_hash"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Now()
			claims := oidcClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: f.profile.Issuer, Subject: "fixture", Audience: jwt.ClaimStrings{f.profile.Audience}, ID: "tls-fixture-access-jti", NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)), IssuedAt: jwt.NewNumericDate(now.Add(-time.Minute)), ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour))}, AuthorizedParty: f.profile.ClientID}
			switch mode {
			case "missing azp":
				claims.AuthorizedParty = ""
			case "wrong azp":
				claims.AuthorizedParty = "other-client"
			case "missing jti":
				claims.ID = ""
			case "blank jti":
				claims.ID = " "
			case "missing nbf":
				claims.NotBefore = nil
			case "future nbf":
				claims.NotBefore = jwt.NewNumericDate(now.Add(time.Hour))
			case "missing iat":
				claims.IssuedAt = nil
			case "future iat":
				claims.IssuedAt = jwt.NewNumericDate(now.Add(time.Hour))
			case "nonce":
				claims.Nonce = "id-token-nonce"
			case "at_hash":
				claims.AccessHash = "id-token-hash"
			}
			raw := f.signClaims(claims)
			_, _, verifyErr := verifyAccess(f.ctx, f.profile, raw)
			session, sessionErr := provider.session(f.ctx, &oidcTokenResponse{AccessToken: raw}, "", "fixture", false)
			if mode == "valid" {
				if verifyErr != nil || sessionErr != nil || session == nil {
					t.Fatal("valid access claims rejected", verifyErr, sessionErr)
				}
			} else if verifyErr == nil || sessionErr == nil || session != nil {
				t.Fatal("invalid access claim shape accepted")
			}
		})
	}
}
