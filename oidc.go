package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	// JWT parsing/validation: github.com/golang-jwt/jwt/v5 v5.3.1, MIT,
	// https://github.com/golang-jwt/jwt (no non-standard runtime dependencies).
	"github.com/golang-jwt/jwt/v5"
)

const humanOIDCIssuer = "https://auth.servas.ai"

// Only tests in this package enable this seam for their own trusted TLS fixtures.
// There is no CLI, environment, or profile setting which enables it.
var testIssuerAllowed = false

type oidcHTTPClientKey struct{}

type oidcProfile struct {
	Issuer, ClientID, Audience, ForgeURL string
	Scopes                               []string
}

type oidcSession struct {
	Issuer, ClientID, Audience, ForgeURL, Subject, AccessToken, RefreshToken, IDToken string
	Expiry                                                                            time.Time
}

func validateProfile(p oidcProfile) error {
	if p.Issuer != humanOIDCIssuer && !testIssuerAllowed {
		return errors.New("OIDC issuer must be https://auth.servas.ai")
	}
	if _, err := oidcHTTPS(p.Issuer); err != nil {
		return errors.New("invalid OIDC issuer")
	}
	if strings.HasSuffix(p.Issuer, "/") {
		return errors.New("invalid OIDC issuer")
	}
	if !oidcIdentifier(p.ClientID) || !oidcIdentifier(p.Audience) || strings.EqualFold(p.Audience, "zitadel") {
		return errors.New("OIDC client and non-management audience identifiers are required")
	}
	if forge, err := oidcHTTPS(p.ForgeURL); err != nil || forge.Path != "" || forge.RawPath != "" {
		return errors.New("invalid Forge URL")
	}
	for _, scope := range p.Scopes {
		if !oidcScope(scope) {
			return errors.New("invalid OIDC scope")
		}
	}
	for _, required := range []string{"openid", "offline_access", "urn:zitadel:iam:org:project:id:" + p.Audience + ":aud"} {
		if !oidcContains(p.Scopes, required) {
			return errors.New("OIDC profile lacks required scopes")
		}
	}
	return nil
}

func oidcScope(s string) bool {
	if s == "" || len(s) > 512 {
		return false
	}
	for _, r := range s {
		if r < 0x21 || r > 0x7e || r == '"' || r == '\\' {
			return false
		}
	}
	return true
}

func oidcIdentifier(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._-:@", r)) {
			return false
		}
	}
	return true
}

func oidcHTTPS(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") || u.Opaque != "" {
		return nil, errors.New("invalid HTTPS endpoint")
	}
	return u, nil
}

func oidcClient(ctx context.Context) *http.Client {
	c := &http.Client{Timeout: 20 * time.Second}
	if injected, ok := ctx.Value(oidcHTTPClientKey{}).(*http.Client); ok && testIssuerAllowed {
		c.Transport = injected.Transport
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("OIDC redirects are forbidden") }
	return c
}

type oidcMetadata struct {
	Issuer           string   `json:"issuer"`
	Authorization    string   `json:"authorization_endpoint"`
	Token            string   `json:"token_endpoint"`
	JWKS             string   `json:"jwks_uri"`
	Revocation       string   `json:"revocation_endpoint"`
	ChallengeMethods []string `json:"code_challenge_methods_supported"`
	IDAlgorithms     []string `json:"id_token_signing_alg_values_supported"`
	ResponseIssuer   bool     `json:"authorization_response_iss_parameter_supported"`
}

type oidcProvider struct {
	profile  oidcProfile
	metadata oidcMetadata
	client   *http.Client
}

func discoverOIDC(ctx context.Context, p oidcProfile) (*oidcProvider, error) {
	if err := validateProfile(p); err != nil {
		return nil, err
	}
	provider := &oidcProvider{profile: p, client: oidcClient(ctx)}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.Issuer+"/.well-known/openid-configuration", nil)
	if err != nil {
		return nil, errors.New("OIDC discovery failed")
	}
	if err := provider.jsonRequest(req, &provider.metadata); err != nil {
		return nil, errors.New("OIDC discovery failed")
	}
	m := provider.metadata
	if m.Issuer != p.Issuer {
		return nil, errors.New("OIDC discovery issuer mismatch")
	}
	origin, _ := url.Parse(p.Issuer)
	for _, endpoint := range []string{m.Authorization, m.Token, m.JWKS, m.Revocation} {
		if endpoint == "" {
			continue
		}
		u, err := oidcHTTPS(endpoint)
		if err != nil || u.Host != origin.Host {
			return nil, errors.New("OIDC discovery contains an unsafe endpoint")
		}
	}
	if m.Authorization == "" || m.Token == "" || m.JWKS == "" {
		return nil, errors.New("OIDC discovery lacks required endpoints")
	}
	return provider, nil
}

// Remote errors are deliberately replaced. OAuth servers may echo credentials,
// authorization codes, or full request URLs in error bodies and transport errors.
func (p *oidcProvider) jsonRequest(req *http.Request, dst any) error {
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return errors.New("OIDC request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("OIDC request rejected")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if err != nil || len(data) > 1024*1024 || json.Unmarshal(data, dst) != nil {
		return errors.New("OIDC response invalid")
	}
	return nil
}

type oidcClaims struct {
	jwt.RegisteredClaims
	Nonce           string `json:"nonce"`
	AuthorizedParty string `json:"azp"`
	AccessHash      string `json:"at_hash"`
}

type oidcJWK struct {
	Kty    string   `json:"kty"`
	Kid    string   `json:"kid"`
	Use    string   `json:"use"`
	Alg    string   `json:"alg"`
	N      string   `json:"n"`
	E      string   `json:"e"`
	KeyOps []string `json:"key_ops"`
}

func (p *oidcProvider) verify(ctx context.Context, raw, audience string) (*oidcClaims, error) {
	invalid := errors.New("OIDC token validation failed")
	if len(raw) == 0 || len(raw) > 64*1024 {
		return nil, invalid
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.metadata.JWKS, nil)
	if err != nil {
		return nil, invalid
	}
	var set struct {
		Keys []oidcJWK `json:"keys"`
	}
	if p.jsonRequest(req, &set) != nil {
		return nil, invalid
	}
	if len(set.Keys) == 0 || len(set.Keys) > 100 {
		return nil, invalid
	}
	keys := make(map[string]oidcJWK, len(set.Keys))
	for _, k := range set.Keys {
		if k.Kid == "" {
			continue
		}
		if _, exists := keys[k.Kid]; exists {
			return nil, invalid
		}
		keys[k.Kid] = k
	}
	claims := &oidcClaims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		if typ, exists := t.Header["typ"]; exists {
			value, ok := typ.(string)
			if !ok || (value != "JWT" && (audience != p.profile.Audience || value != "at+jwt")) {
				return nil, invalid
			}
		}
		if t.Method != jwt.SigningMethodRS256 {
			return nil, invalid
		}
		for _, header := range []string{"jku", "x5u", "jwk", "crit"} {
			if _, exists := t.Header[header]; exists {
				return nil, invalid
			}
		}
		kid, ok := t.Header["kid"].(string)
		if !ok || kid == "" {
			return nil, invalid
		}
		k, ok := keys[kid]
		if !ok || k.Kty != "RSA" || (k.Use != "" && k.Use != "sig") || (k.Alg != "" && k.Alg != "RS256") {
			return nil, invalid
		}
		if len(k.KeyOps) > 0 && !oidcContains(k.KeyOps, "verify") {
			return nil, invalid
		}
		nb, e1 := base64.RawURLEncoding.Strict().DecodeString(k.N)
		eb, e2 := base64.RawURLEncoding.Strict().DecodeString(k.E)
		if e1 != nil || e2 != nil || len(nb) < 256 || len(nb) > 1024 || len(eb) == 0 || len(eb) > 4 {
			return nil, invalid
		}
		n := new(big.Int).SetBytes(nb)
		e := new(big.Int).SetBytes(eb).Int64()
		if n.BitLen() < 2048 || e < 3 || e > 2147483647 || e%2 == 0 {
			return nil, invalid
		}
		return &rsa.PublicKey{N: n, E: int(e)}, nil
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(p.profile.Issuer), jwt.WithAudience(audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithStrictDecoding())
	if err != nil || token == nil || !token.Valid || claims.Subject == "" || claims.ExpiresAt == nil {
		return nil, invalid
	}
	return claims, nil
}

func verifyAccess(ctx context.Context, p oidcProfile, raw string) (string, time.Time, error) {
	provider, err := discoverOIDC(ctx, p)
	if err != nil {
		return "", time.Time{}, err
	}
	claims, err := provider.verify(ctx, raw, p.Audience)
	if err != nil {
		return "", time.Time{}, err
	}
	return claims.Subject, claims.ExpiresAt.Time, nil
}

func oidcContains(v []string, w string) bool {
	for _, s := range v {
		if s == w {
			return true
		}
	}
	return false
}
func oidcRandom() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", errors.New("OIDC secure randomness unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

type oidcTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	Error        string `json:"error"`
}

func (p *oidcProvider) token(ctx context.Context, form url.Values) (*oidcTokenResponse, error) {
	form.Set("client_id", p.profile.ClientID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.metadata.Token, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, errors.New("OIDC token request failed")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var result oidcTokenResponse
	if p.jsonRequest(req, &result) != nil || result.Error != "" || result.AccessToken == "" || !strings.EqualFold(result.TokenType, "Bearer") {
		return nil, errors.New("OIDC token request failed")
	}
	return &result, nil
}

func (p *oidcProvider) session(ctx context.Context, t *oidcTokenResponse, nonce, subject string, requireID bool) (*oidcSession, error) {
	access, err := p.verify(ctx, t.AccessToken, p.profile.Audience)
	if err != nil {
		return nil, err
	}
	if subject != "" && access.Subject != subject {
		return nil, errors.New("OIDC subject changed")
	}
	if t.IDToken == "" && requireID {
		return nil, errors.New("OIDC ID token missing")
	}
	if t.IDToken != "" {
		id, err := p.verify(ctx, t.IDToken, p.profile.ClientID)
		if err != nil {
			return nil, err
		}
		if id.AccessHash != "" {
			hash := sha256.Sum256([]byte(t.AccessToken))
			expected := base64.RawURLEncoding.EncodeToString(hash[:len(hash)/2])
			if subtle.ConstantTimeCompare([]byte(id.AccessHash), []byte(expected)) != 1 {
				return nil, errors.New("OIDC ID token validation failed")
			}
		}
		if id.Subject != access.Subject || (nonce != "" && subtle.ConstantTimeCompare([]byte(id.Nonce), []byte(nonce)) != 1) || (id.AuthorizedParty != "" && id.AuthorizedParty != p.profile.ClientID) || (len(id.Audience) > 1 && id.AuthorizedParty != p.profile.ClientID) {
			return nil, errors.New("OIDC ID token validation failed")
		}
	}
	profile := p.profile
	return &oidcSession{Issuer: profile.Issuer, ClientID: profile.ClientID, Audience: profile.Audience, ForgeURL: profile.ForgeURL, Subject: access.Subject, AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, IDToken: t.IDToken, Expiry: access.ExpiresAt.Time}, nil
}

func loginOIDC(ctx context.Context, p oidcProfile, open func(string)) (*oidcSession, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	provider, err := discoverOIDC(ctx, p)
	if err != nil {
		return nil, err
	}
	if !oidcContains(provider.metadata.ChallengeMethods, "S256") || !oidcContains(provider.metadata.IDAlgorithms, "RS256") {
		return nil, errors.New("OIDC provider must support PKCE S256 and RS256")
	}
	if open == nil {
		return nil, errors.New("OIDC browser opener is required")
	}
	state, err := oidcRandom()
	if err != nil {
		return nil, err
	}
	nonce, err := oidcRandom()
	if err != nil {
		return nil, err
	}
	verifier, err := oidcRandom()
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("OIDC callback listener unavailable")
	}
	defer listener.Close()
	redirect := "http://" + listener.Addr().String() + "/callback"
	type callback struct {
		code string
		err  error
	}
	callbacks := make(chan callback, 1)
	var once sync.Once
	finish := func(v callback) { once.Do(func() { callbacks <- v }) }
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		fail := func() {
			http.Error(w, "Login failed. Return to the terminal.", http.StatusBadRequest)
			_ = http.NewResponseController(w).Flush()
			finish(callback{err: errors.New("OIDC callback rejected")})
		}
		if r.Method != http.MethodGet || r.Host != listener.Addr().String() || len(r.URL.RawQuery) > 16*1024 {
			fail()
			return
		}
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			fail()
			return
		}
		for _, values := range q {
			if len(values) != 1 {
				fail()
				return
			}
		}
		if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 || q.Get("error") != "" || q.Get("code") == "" {
			fail()
			return
		}
		issuer := q.Get("iss")
		if (issuer != "" && issuer != p.Issuer) || (provider.metadata.ResponseIssuer && issuer != p.Issuer) {
			fail()
			return
		}
		_, _ = io.WriteString(w, "Login received. Return to the terminal.")
		_ = http.NewResponseController(w).Flush()
		finish(callback{code: q.Get("code")})
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 32 * 1024}
	defer server.Close()
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			finish(callback{err: errors.New("OIDC callback listener failed")})
		}
	}()
	auth, _ := url.Parse(provider.metadata.Authorization)
	q := auth.Query()
	q.Set("response_type", "code")
	q.Set("client_id", p.ClientID)
	q.Set("redirect_uri", redirect)
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge_method", "S256")
	hash := sha256.Sum256([]byte(verifier))
	q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(hash[:]))
	scopes := append([]string(nil), p.Scopes...)
	if !oidcContains(scopes, "openid") {
		scopes = append(scopes, "openid")
	}
	q.Set("scope", strings.Join(scopes, " "))
	q.Set("audience", p.Audience)
	auth.RawQuery = q.Encode()
	// The caller controls how the authorization URL is opened. Tokens and callback
	// codes are never printed or included in returned errors.
	go open(auth.String())
	var result callback
	select {
	case result = <-callbacks:
	case <-ctx.Done():
		return nil, errors.New("OIDC login cancelled or timed out")
	}
	if result.err != nil {
		return nil, result.err
	}
	tokens, err := provider.token(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {result.code}, "redirect_uri": {redirect}, "code_verifier": {verifier}})
	if err != nil {
		return nil, err
	}
	return provider.session(ctx, tokens, nonce, "", true)
}

func oidcSessionMatches(p oidcProfile, s *oidcSession) bool {
	return s != nil && s.Issuer == p.Issuer && s.ClientID == p.ClientID && s.Audience == p.Audience && s.ForgeURL == p.ForgeURL && s.Subject != ""
}

func refreshOIDC(ctx context.Context, p oidcProfile, s *oidcSession) (*oidcSession, error) {
	if !oidcSessionMatches(p, s) || s.RefreshToken == "" {
		return nil, errors.New("OIDC session does not match profile or has no refresh token")
	}
	provider, err := discoverOIDC(ctx, p)
	if err != nil {
		return nil, err
	}
	tokens, err := provider.token(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {s.RefreshToken}})
	if err != nil {
		return nil, err
	}
	result, err := provider.session(ctx, tokens, "", s.Subject, false)
	if err != nil {
		return nil, err
	}
	if result.RefreshToken == "" {
		result.RefreshToken = s.RefreshToken
	}
	// An absent new ID token is normal on refresh; preserve the previously verified
	// identity token for storage, never as evidence that a new access token is valid.
	if result.IDToken == "" {
		result.IDToken = s.IDToken
	}
	return result, nil
}

func revokeOIDC(ctx context.Context, p oidcProfile, s *oidcSession) error {
	if !oidcSessionMatches(p, s) {
		return errors.New("OIDC session does not match profile")
	}
	provider, err := discoverOIDC(ctx, p)
	if err != nil {
		return err
	}
	if provider.metadata.Revocation == "" {
		return errors.New("OIDC provider does not support revocation")
	}
	failed := false
	for _, v := range []struct{ token, hint string }{{s.RefreshToken, "refresh_token"}, {s.AccessToken, "access_token"}} {
		if v.token == "" {
			continue
		}
		form := url.Values{"token": {v.token}, "token_type_hint": {v.hint}, "client_id": {p.ClientID}}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.metadata.Revocation, strings.NewReader(form.Encode()))
		if err != nil {
			return errors.New("OIDC revocation failed")
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := provider.client.Do(req)
		if err != nil {
			failed = true
			continue
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			failed = true
		}
	}
	if failed {
		return errors.New("OIDC revocation failed")
	}
	return nil
}
