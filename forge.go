package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Forge-Brain: liegt das Brain nicht auf GitHub, sondern auf einer eigenen Forge (Forgejo, z. B.
// auf dem NAS), steht das in <root>/.brain/forge.yml:
//
//	typ: forgejo
//	url: https://git.example.com:8443
//	org: brain
//
// Fehlt die Datei, bleibt alles wie bisher (GitHub über gh). Mit der Datei findet `brain sync` die
// Workspaces through the Forgejo API using the verified OIDC session created
// by `brain login`; GitHub update discovery is disabled.

const forgeFile = "forge.yml"

type forgeConfig struct {
	Typ, URL, Org string
}

// readForge liefert nil, wenn <root>/.brain/forge.yml fehlt.
func readForge(root string) (*forgeConfig, error) {
	p := filepath.Join(root, ".brain", forgeFile)
	if !isFile(p) {
		return nil, nil
	}
	f := simpleYAML(p)
	c := &forgeConfig{Typ: f.get("typ"), URL: strings.TrimRight(f.get("url"), "/"), Org: f.get("org")}
	if c.Typ != "forgejo" {
		return nil, fmt.Errorf(".brain/%s: unsupported type (expected forgejo)", forgeFile)
	}
	if u, err := url.Parse(c.URL); err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf(".brain/%s: invalid HTTPS Forge origin", forgeFile)
	}
	if c.Org == "" || strings.ContainsAny(c.Org, "/?#") {
		return nil, fmt.Errorf(".brain/%s: missing or invalid organization", forgeFile)
	}
	return c, nil
}

// forgeActive: true, wenn das Brain unter root ein Forge-Brain ist (auch bei fehlerhafter Datei).
func forgeActive(root string) bool { return isFile(filepath.Join(root, ".brain", forgeFile)) }

// gitCredential supplies only a verified OIDC access token, never a Git store/PAT.
func gitCredential(forgeURL string) (string, string, error) {
	token, err := currentAccess(forgeURL)
	return "oidc", token, err
}

type forgeRepo struct {
	Name     string   `json:"name"`
	CloneURL string   `json:"clone_url"`
	Topics   []string `json:"topics"`
}

var forgeHTTP = &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func forgeGet(rawURL, token string, v any) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("invalid HTTPS Forge URL")
	}
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "brain-cli/"+version)
	resp, err := forgeHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("Forge request failed (TLS/network)")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET %s: HTTP %d", req.URL.Path, resp.StatusCode)
	}
	return decodeForgeResponse(resp.Body, v)
}

// forgejoCells listet alle Repos der Org mit Topic brain-cell, die das Token lesen darf
// (GET /api/v1/orgs/<org>/repos, seitenweise).
func forgejoCells(c *forgeConfig, token string) ([]forgeRepo, error) {
	const limit = 50
	var cells []forgeRepo
	for page := 1; page <= 200; page++ {
		var repos []forgeRepo
		u := c.URL + "/api/v1/orgs/" + url.PathEscape(c.Org) + "/repos?limit=" + strconv.Itoa(limit) + "&page=" + strconv.Itoa(page)
		if err := forgeGet(u, token, &repos); err != nil {
			return nil, err
		}
		for _, r := range repos {
			for _, t := range r.Topics {
				if t == "brain-cell" && r.Name != "" && r.CloneURL != "" {
					if validForgeClone(c.URL, r.CloneURL) {
						cells = append(cells, r)
					}
					break
				}
			}
		}
		if len(repos) < limit {
			break
		}
	}
	return cells, nil
}

// forgeCellLines liefert die Zellen eines Forge-Brains im Format der gh-Discovery ("name clone_url").
func forgeCellLines(c *forgeConfig) (string, error) {
	_, token, err := gitCredential(c.URL)
	if err != nil {
		return "", err
	}
	cells, err := forgejoCells(c, token)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, r := range cells {
		b.WriteString(r.Name + " " + r.CloneURL + "\n")
	}
	return b.String(), nil
}

func openBrowser(u string) {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	case "darwin":
		c = exec.Command("open", u)
	default:
		if _, err := exec.LookPath("xdg-open"); err != nil {
			return
		}
		c = exec.Command("xdg-open", u)
	}
	_ = c.Start()
}

func decodeForgeResponse(r io.Reader, v any) error {
	if err := json.NewDecoder(io.LimitReader(r, 32<<20)).Decode(v); err != nil {
		return fmt.Errorf("invalid Forge response")
	}
	return nil
}

func validForgeClone(origin, raw string) bool {
	u, e := url.Parse(raw)
	if e != nil {
		return false
	}
	o, e := url.Parse(origin)
	if e != nil {
		return false
	}
	return u.Scheme == "https" && u.Host == o.Host && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.Path != "" && !strings.ContainsAny(u.Path, "\\\r\n")
}
