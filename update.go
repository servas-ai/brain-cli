package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Update-Prüfung: vergleicht die Framework-Version des Brains (BRAIN_VERSION) und die
// Versionen von brain, sharepointcli, qdrantcli mit dem neuesten Stand auf GitHub.
// Ohne Netz oder bei Fehlern wird nichts gemeldet; Ergebnisse werden 24 h zwischengespeichert.

const (
	templateRepo      = "servas-ai/brain"
	frameworkFile     = "BRAIN_VERSION"
	updateCacheMaxAge = 24 * time.Hour
)

var toolRepos = []struct{ tool, repo string }{
	{"brain", "servas-ai/brain-cli"},
	{"sharepointcli", "servas-ai/sharepointcli"},
	{"qdrantcli", "servas-ai/qdrantcli"},
}

type updateCache struct {
	Checked time.Time         `json:"checked"`
	Latest  map[string]string `json:"latest"`
}

func cachePath() string {
	d, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "brain", "updates.json")
}

func latestVersions(force bool) map[string]string {
	cp := cachePath()
	if !force && cp != "" {
		if b, err := os.ReadFile(cp); err == nil {
			var c updateCache
			if json.Unmarshal(b, &c) == nil && time.Since(c.Checked) < updateCacheMaxAge && len(c.Latest) > 0 {
				return c.Latest
			}
		}
	}
	client := &http.Client{Timeout: 4 * time.Second}
	latest := map[string]string{}
	if v := httpText(client, "https://raw.githubusercontent.com/"+templateRepo+"/main/"+frameworkFile); v != "" {
		latest["framework"] = v
	}
	for _, t := range toolRepos {
		body := httpText(client, "https://api.github.com/repos/"+t.repo+"/releases/latest")
		var rel struct {
			Tag string `json:"tag_name"`
		}
		if body != "" && json.Unmarshal([]byte(body), &rel) == nil && rel.Tag != "" {
			latest[t.tool] = strings.TrimPrefix(rel.Tag, "v")
		}
	}
	if len(latest) > 0 && cp != "" {
		os.MkdirAll(filepath.Dir(cp), 0o755)
		if b, err := json.Marshal(updateCache{Checked: time.Now(), Latest: latest}); err == nil {
			os.WriteFile(cp, b, 0o644)
		}
	}
	return latest
}

func httpText(c *http.Client, url string) string {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "brain-cli/"+version)
	resp, err := c.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ""
	}
	buf := make([]byte, 64*1024)
	n, _ := resp.Body.Read(buf)
	var all []byte
	all = append(all, buf[:n]...)
	for n > 0 {
		n, _ = resp.Body.Read(buf)
		all = append(all, buf[:n]...)
	}
	return strings.TrimSpace(string(all))
}

func localToolVersion(tool string) string {
	if tool == "brain" {
		return version
	}
	p, err := exec.LookPath(tool)
	if err != nil {
		return ""
	}
	out, err := exec.Command(p, "version").Output()
	if err != nil {
		return ""
	}
	f := strings.Fields(strings.TrimSpace(string(out)))
	if len(f) == 0 {
		return ""
	}
	return strings.TrimPrefix(f[len(f)-1], "v")
}

// newer reports whether a is a higher version than b (numeric dot-separated).
func newer(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// updateNotices returns human-readable lines for every component that has a newer version.
func updateNotices(root string, force bool) []string {
	if os.Getenv("BRAIN_NO_UPDATE_CHECK") == "1" {
		return nil
	}
	latest := latestVersions(force)
	var out []string
	if lv, ok := latest["framework"]; ok {
		local := "unbekannt (keine BRAIN_VERSION)"
		if b, err := os.ReadFile(filepath.Join(root, frameworkFile)); err == nil {
			local = strings.TrimSpace(string(b))
		}
		if local == "unbekannt (keine BRAIN_VERSION)" || newer(lv, local) {
			out = append(out, fmt.Sprintf("Framework (Vorlage %s): lokal %s, aktuell %s → Anleitung `public.brain.faq.framework-aktualisieren` bzw. https://github.com/%s/blob/main/public/brain/faq/framework-aktualisieren/README.md", templateRepo, local, lv, templateRepo))
		}
	}
	for _, t := range toolRepos {
		lv, ok := latest[t.tool]
		if !ok {
			continue
		}
		local := localToolVersion(t.tool)
		if local == "" || local == "dev" {
			continue
		}
		if newer(lv, local) {
			out = append(out, fmt.Sprintf("%s: installiert %s, aktuell %s → Anleitung `public.brain.faq.tools-aktualisieren`", t.tool, local, lv))
		}
	}
	return out
}

func appendUpdatesToPerspektive(root string, notices []string) {
	p := filepath.Join(root, "_perspektive.md")
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	if len(notices) == 0 {
		fmt.Fprintf(f, "\n## Updates\n\nAlles aktuell (Stand %s).\n", time.Now().Format("2006-01-02"))
		return
	}
	fmt.Fprintf(f, "\n## Updates verfügbar\n\nSag es deinem Menschen zu Beginn der Sitzung und biete an, das Update auszuführen:\n\n")
	for _, n := range notices {
		fmt.Fprintf(f, "- %s\n", n)
	}
}

func cmdUpdateCheck(args []string) int {
	root, err := findRoot("")
	if err != nil {
		root = "."
	}
	notices := updateNotices(root, true)
	if len(notices) == 0 {
		fmt.Println("brain update-check: alles aktuell")
		return 0
	}
	for _, n := range notices {
		fmt.Println("Update verfügbar: " + n)
	}
	return 0
}
