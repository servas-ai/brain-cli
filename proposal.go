package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
)

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*/[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

func cmdPropose(args []string) int {
	fs := flag.NewFlagSet("propose", flag.ContinueOnError)
	repo := fs.String("repo", "", "Existing owner/repository")
	branch := fs.String("branch", "", "Pushed proposal branch")
	base := fs.String("base", "main", "Target branch")
	title := fs.String("title", "", "Proposal title (no credentials)")
	bodyFile := fs.String("body-file", "", "UTF-8 description file (no credentials)")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return 2
	}
	if !repoPattern.MatchString(*repo) || *branch == "" || *base == "" || *title == "" {
		fmt.Fprintln(os.Stderr, "Usage: brain propose --repo owner/repo --branch proposal --title 'Title' [--base main] [--body-file description.md]")
		return 2
	}
	body := ""
	if *bodyFile != "" {
		f, err := os.Open(*bodyFile)
		if err != nil {
			return authError(errors.New("cannot read proposal description"))
		}
		b, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
		f.Close()
		if err != nil || len(b) > 64<<10 {
			return authError(errors.New("proposal description must be at most 64 KiB"))
		}
		body = string(b)
	}
	p, err := savedProfile()
	if err != nil {
		return authError(err)
	}
	token, err := currentAccess(p.ForgeURL)
	if err != nil {
		return authError(err)
	}
	link, err := createProposal(p.ForgeURL, token, *repo, *branch, *base, *title, body)
	if err != nil {
		return authError(err)
	}
	fmt.Println(link)
	return 0
}

func createProposal(origin, token, repo, branch, base, title, body string) (string, error) {
	if !repoPattern.MatchString(repo) || !validForgeClone(origin, origin+"/"+repo) {
		return "", errors.New("invalid proposal repository")
	}
	data, _ := json.Marshal(map[string]string{"head": branch, "base": base, "title": title, "body": body})
	req, err := http.NewRequest("POST", origin+"/api/v1/repos/"+repo+"/pulls", bytes.NewReader(data))
	if err != nil {
		return "", errors.New("invalid proposal request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := forgeHTTP.Do(req)
	if err != nil {
		return "", errors.New("proposal request failed (TLS/network)")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		return "", fmt.Errorf("proposal rejected: HTTP %d", resp.StatusCode)
	}
	var result struct {
		HTMLURL string `json:"html_url"`
	}
	if err := decodeForgeResponse(resp.Body, &result); err != nil {
		return "", err
	}
	u, err := url.Parse(result.HTMLURL)
	if err != nil || !validForgeClone(origin, result.HTMLURL) || u.RawQuery != "" {
		return "", errors.New("invalid proposal response URL")
	}
	return result.HTMLURL, nil
}

const authGuide = `Human OIDC login and first proposal

Install the versioned archive and check its SHA256SUMS entry. Import the
operator-provided CA into your OS/Git trust store when using private TLS.
Prerequisite: the operator has supplied an existing public native Zitadel
client ID, resource project audience, Forge HTTPS origin and verified subject
mapping. The KIRA OIDC API/Git adapter must be installed by the KIRA owner.
This client does not create clients, grants, identities or access keys.

  brain auth configure --url https://git.example.invalid --client-id CLIENT_ID --audience RESOURCE_PROJECT_ID --scopes 'openid offline_access urn:zitadel:iam:org:project:id:RESOURCE_PROJECT_ID:aud'
  brain login
  brain auth status
  brain git -- clone https://git.example.invalid/brain/brain.git
  cd brain
  brain git -- switch -c proposal/first-change
  # Edit a Brain document. Set your public Git name/email if required.
  brain git -- add README.md
  brain git -- commit -m 'First proposal'
  brain git -- push -u origin proposal/first-change
  brain propose --repo brain/brain --branch proposal/first-change --title 'First proposal'
  brain auth logout

Use brain git for Clone/Fetch/Push. It isolates credential helpers for this
invocation and supplies tokens only to the configured HTTPS Forge origin.
It never installs a global helper. Refresh is automatic; failures require a
new browser login. Status prints secret-free JSON. Exit codes: 0 success,
1 authentication/network/operation failure, 2 invalid command arguments.
Never pass tokens on stdin/argv or use Git credential store as a workaround.
Issuer is fixed to https://auth.servas.ai. Pocket-ID browser login is separate.
See docs/INSTALL.md and docs/OIDC-CONTRACT.md for activation prerequisites.
`
