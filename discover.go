package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// discoverCells findet über die GitHub CLI alle Repos der Organisation des Brain-Repos mit dem
// Topic brain-cell, die der Login lesen darf, und klont fehlende nach workspaces/<name>.
// Zellen, die lokal liegen, aber nicht mehr gelistet werden, werden nur gemeldet (nie gelöscht).
func discoverCells(root string) (added, gone []string) {
	gh, err := exec.LookPath("gh")
	if err != nil {
		return nil, nil
	}
	url, err := runTimeout(10, "git", "-C", root, "remote", "get-url", "origin")
	if err != nil {
		return nil, nil
	}
	org := orgFromURL(strings.TrimSpace(url))
	if org == "" {
		return nil, nil
	}
	out, err := runTimeout(30, gh, "api", "--paginate", "orgs/"+org+"/repos?per_page=100",
		"--jq", `.[] | select((.topics // []) | index("brain-cell")) | .name + " " + .clone_url`)
	if err != nil {
		// privater Account statt Organisation
		out, err = runTimeout(30, gh, "api", "--paginate", "user/repos?per_page=100&affiliation=owner,collaborator,organization_member",
			"--jq", `.[] | select(.owner.login == "`+org+`") | select((.topics // []) | index("brain-cell")) | .name + " " + .clone_url`)
		if err != nil {
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

func orgFromURL(u string) string {
	u = strings.TrimSuffix(u, ".git")
	if i := strings.Index(u, "github.com"); i >= 0 {
		rest := strings.TrimLeft(u[i+len("github.com"):], ":/")
		if j := strings.Index(rest, "/"); j > 0 {
			return rest[:j]
		}
	}
	return ""
}

func reportDiscovery(added, gone []string) {
	for _, a := range added {
		fmt.Println("brain sync: neuer Workspace eingehängt: " + a)
	}
	for _, g := range gone {
		fmt.Println("brain sync: Workspace nicht mehr freigegeben (lokal belassen, bitte prüfen): " + g)
	}
}
