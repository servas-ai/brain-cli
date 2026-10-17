package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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
// Workspaces über die Forgejo-API, das Token kommt aus `git credential fill` (abgelegt mit
// `brain login`), und die Update-Prüfung gegen GitHub ist aus.

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
		return nil, fmt.Errorf(".brain/%s: typ %q wird nicht unterstützt (nur forgejo)", forgeFile, c.Typ)
	}
	if u, err := url.Parse(c.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf(".brain/%s: url %q ist keine http(s)-URL", forgeFile, c.URL)
	}
	if c.Org == "" || strings.ContainsAny(c.Org, "/?#") {
		return nil, fmt.Errorf(".brain/%s: org %q fehlt oder ist ungültig", forgeFile, c.Org)
	}
	return c, nil
}

// forgeActive: true, wenn das Brain unter root ein Forge-Brain ist (auch bei fehlerhafter Datei).
func forgeActive(root string) bool { return isFile(filepath.Join(root, ".brain", forgeFile)) }

// gitCredential fragt git nach den Zugangsdaten für die Forge-URL (ohne Rückfrage am Terminal).
func gitCredential(forgeURL string) (user, pass string, err error) {
	u, err := url.Parse(forgeURL)
	if err != nil {
		return "", "", err
	}
	c := exec.Command("git", "credential", "fill")
	c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never")
	c.Stdin = strings.NewReader("protocol=" + u.Scheme + "\nhost=" + u.Host + "\n\n")
	out, err := c.Output()
	if err != nil {
		return "", "", fmt.Errorf("kein Zugang für %s hinterlegt (erst `brain login`)", u.Host)
	}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, _ := strings.Cut(strings.TrimRight(line, "\r"), "=")
		switch k {
		case "username":
			user = v
		case "password":
			pass = v
		}
	}
	if pass == "" {
		return "", "", fmt.Errorf("kein Token für %s hinterlegt (erst `brain login`)", u.Host)
	}
	return user, pass, nil
}

type forgeRepo struct {
	Name     string   `json:"name"`
	CloneURL string   `json:"clone_url"`
	Topics   []string `json:"topics"`
}

var forgeHTTP = &http.Client{Timeout: 20 * time.Second}

func forgeGet(rawURL, token string, v any) error {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "brain-cli/"+version)
	resp, err := forgeHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET %s: HTTP %d", req.URL.Path, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(v)
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
					cells = append(cells, r)
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

// ---------------- brain login ----------------

const loginUsage = "Aufruf: brain login [--url <forge-url>] [--root <dir>] [--no-open]   (Token auf stdin)"

func cmdLogin(args []string) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	forgeURL := fs.String("url", "", "Forge-URL (Standard: url aus .brain/forge.yml)")
	root := fs.String("root", "", "Brain-Root")
	noOpen := fs.Bool("no-open", false, "keinen Browser öffnen")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	base := strings.TrimRight(*forgeURL, "/")
	if base == "" {
		r, err := findRoot(*root)
		if err == nil {
			var c *forgeConfig
			if c, err = readForge(r); err == nil && c != nil {
				base = c.URL
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "brain:", err)
			return 1
		}
	}
	u, err := url.Parse(base)
	if base == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		fmt.Fprintln(os.Stderr, "brain login: keine Forge-URL (--url oder .brain/forge.yml)\n"+loginUsage)
		return 2
	}
	page := base + "/user/settings/applications"
	tty := isTerminal(os.Stdin)
	fmt.Fprintf(os.Stderr, "brain login: Token erstellen unter %s\n  (Rechte: write:repository, read:user, read:organization)\n", page)
	if tty && !*noOpen {
		openBrowser(page)
	}
	if tty {
		fmt.Fprint(os.Stderr, "Token: ")
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	token := strings.TrimSpace(line)
	if token == "" || (err != nil && err != io.EOF) {
		fmt.Fprintln(os.Stderr, "brain login: kein Token gelesen")
		return 1
	}
	var me struct {
		Login string `json:"login"`
	}
	if err := forgeGet(base+"/api/v1/user", token, &me); err != nil || me.Login == "" {
		fmt.Fprintf(os.Stderr, "brain login: Token wird von %s nicht angenommen (%v)\n", u.Host, err)
		return 1
	}
	if h, _ := runTimeout(10, "git", "config", "--get", "credential.helper"); strings.TrimSpace(h) == "" {
		if out, err := runTimeout(10, "git", "config", "--global", "credential.helper", "store"); err != nil {
			fmt.Fprintf(os.Stderr, "brain login: git config credential.helper store fehlgeschlagen: %v %s\n", err, out)
			return 1
		}
		fmt.Fprintln(os.Stderr, "brain login: git credential.helper war leer, gesetzt auf store (~/.git-credentials)")
	}
	c := exec.Command("git", "credential", "approve")
	c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	c.Stdin = strings.NewReader("protocol=" + u.Scheme + "\nhost=" + u.Host + "\nusername=" + me.Login + "\npassword=" + token + "\n\n")
	if out, err := c.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "brain login: git credential approve fehlgeschlagen: %v %s\n", err, out)
		return 1
	}
	if _, p, err := gitCredential(base); err != nil || p != token {
		fmt.Fprintln(os.Stderr, "brain login: Token wurde vom credential.helper nicht gespeichert")
		return 1
	}
	fmt.Printf("brain login: angemeldet als %s bei %s\n", me.Login, u.Host)
	return 0
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
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
