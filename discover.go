package main

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// discoverCells findet über die GitHub CLI alle Repos der Organisation des Brain-Repos mit dem
// Topic brain-cell, die der Login lesen darf, und klont fehlende nach workspaces/<name>.
// Zellen, die lokal liegen, aber nicht mehr gelistet werden, werden nur gemeldet (nie gelöscht).
//
// Liegt .brain/forge.yml im Root (Forge-Brain, siehe forge.go), kommt die Liste stattdessen aus der
// Forgejo-API; gh wird dann nicht gebraucht.
func discoverCells(root string) (added, gone []string) {
	var out string
	if fc, err := readForge(root); err != nil || fc != nil {
		if err == nil {
			out, err = forgeCellLines(fc)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "brain: warning: workspace discovery failed:", err)
			return nil, nil
		}
	} else {
		var ok bool
		if out, ok = githubCellLines(root); !ok {
			return nil, nil
		}
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		name, clone := f[0], f[1]
		seen[name] = true
		if exists(filepath.Join(root, "workspaces", name)) {
			continue
		}
		if cmdCloneCell([]string{"--root", root, clone}) == 0 {
			added = append(added, name)
		}
	}
	if len(seen) > 0 {
		for _, n := range listNames(filepath.Join(root, "workspaces")) {
			if isDir(filepath.Join(root, "workspaces", n, ".git")) && !seen[n] {
				gone = append(gone, n)
			}
		}
	}
	return
}

// githubCellLines: Zellen über die GitHub CLI ("name clone_url" je Zeile); ok=false ohne gh/Org.
func githubCellLines(root string) (string, bool) {
	gh, err := exec.LookPath("gh")
	if err != nil {
		return "", false
	}
	url, err := runTimeout(10, "git", "-C", root, "remote", "get-url", "origin")
	if err != nil {
		return "", false
	}
	org := orgFromURL(strings.TrimSpace(url))
	if org == "" {
		return "", false
	}
	out, err := runTimeout(30, gh, "api", "--paginate", "orgs/"+org+"/repos?per_page=100",
		"--jq", `.[] | select((.topics // []) | index("brain-cell")) | .name + " " + .clone_url`)
	if err != nil {
		// privater Account statt Organisation
		out, err = runTimeout(30, gh, "api", "--paginate", "user/repos?per_page=100&affiliation=owner,collaborator,organization_member",
			"--jq", `.[] | select(.owner.login == "`+org+`") | select((.topics // []) | index("brain-cell")) | .name + " " + .clone_url`)
		if err != nil {
			return "", false
		}
	}
	return out, true
}

func orgFromURL(u string) string {
	// Keep recognizing existing GitHub metadata, without accepting hosts
	// which merely contain the text github.com.
	if strings.HasPrefix(u, "git@github.com:") {
		u = "https://github.com/" + strings.TrimPrefix(u, "git@github.com:")
	}
	if !githubHTTPS(u) {
		return ""
	}
	parsed, _ := url.Parse(u)
	parts := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
	if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return parts[0]
	}
	return ""
}

func reportDiscovery(added, gone []string) {
	for _, a := range added {
		fmt.Println("brain sync: new workspace mounted: " + a)
	}
	for _, g := range gone {
		fmt.Println("brain sync: workspace access no longer granted (kept locally; please review): " + g)
	}
}
