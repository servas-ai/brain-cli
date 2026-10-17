package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const testToken = "tok-0123456789abcdef"

// fakeForgejo spielt die zwei Endpunkte, die brain-cli nutzt: /api/v1/orgs/<org>/repos (seitenweise,
// limit/page wie Forgejo) und /api/v1/user. Ohne gültiges Token: 404 wie bei einer privaten Org.
type fakeForgejo struct {
	org   string
	repos []forgeRepo
	mu    sync.Mutex
	pages []string
}

func (f *fakeForgejo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "token "+testToken {
		http.NotFound(w, r)
		return
	}
	switch r.URL.Path {
	case "/api/v1/user":
		json.NewEncoder(w).Encode(map[string]any{"id": 7, "login": "alice"})
	case "/api/v1/orgs/" + f.org + "/repos":
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if limit <= 0 || limit > 50 {
			limit = 30
		}
		if page < 1 {
			page = 1
		}
		f.mu.Lock()
		f.pages = append(f.pages, r.URL.Query().Get("page"))
		f.mu.Unlock()
		lo, hi := (page-1)*limit, page*limit
		if lo > len(f.repos) {
			lo = len(f.repos)
		}
		if hi > len(f.repos) {
			hi = len(f.repos)
		}
		json.NewEncoder(w).Encode(f.repos[lo:hi])
	default:
		http.NotFound(w, r)
	}
}

// isolatedGit: eigenes HOME und eigene globale git-Konfiguration je Test (kein Helper gesetzt).
func isolatedGit(t *testing.T) (home, gitconfig string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git fehlt")
	}
	home = t.TempDir()
	gitconfig = filepath.Join(home, "gitconfig")
	if err := os.WriteFile(gitconfig, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", gitconfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	return
}

func mustGit(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeForge(t *testing.T, root, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".brain"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".brain", "forge.yml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// withStdin ersetzt os.Stdin für fn durch eine Pipe mit s.
func withStdin(t *testing.T, s string, fn func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() { w.WriteString(s); w.Close() }()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old; r.Close() }()
	fn()
}

func TestReadForge(t *testing.T) {
	root := t.TempDir()
	if c, err := readForge(root); c != nil || err != nil {
		t.Fatalf("ohne forge.yml: %v %v", c, err)
	}
	if forgeActive(root) {
		t.Fatal("forgeActive ohne Datei")
	}
	writeForge(t, root, "# Forge-Brain\ntyp: forgejo\nurl: https://git.example.com:8443/\norg: brain\n")
	c, err := readForge(root)
	if err != nil || c == nil || c.URL != "https://git.example.com:8443" || c.Org != "brain" {
		t.Fatalf("forge.yml: %+v %v", c, err)
	}
	for _, bad := range []string{"typ: gitlab\nurl: https://x\norg: o\n", "typ: forgejo\nurl: ftp://x\norg: o\n", "typ: forgejo\nurl: https://x\n"} {
		writeForge(t, root, bad)
		if _, err := readForge(root); err == nil {
			t.Fatalf("kein Fehler für %q", bad)
		}
	}
}

func TestOrgFromURLUnchanged(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/servas-ai/brain.git": "servas-ai",
		"git@github.com:servas-ai/brain.git":     "servas-ai",
		"https://git.example.com/brain/brain":    "",
	} {
		if got := orgFromURL(in); got != want {
			t.Errorf("orgFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestForgejoCellsPaginatedTopicFilter(t *testing.T) {
	f := &fakeForgejo{org: "brain"}
	want := map[string]bool{}
	for i := 0; i < 120; i++ {
		r := forgeRepo{Name: "r" + strconv.Itoa(i), CloneURL: "https://git.example.com/brain/r" + strconv.Itoa(i) + ".git"}
		switch {
		case i%3 == 0:
			r.Topics = []string{"x", "brain-cell"}
			want[r.Name] = true
		case i%3 == 1:
			r.Topics = []string{"brain"}
		}
		f.repos = append(f.repos, r)
	}
	srv := httptest.NewServer(f)
	defer srv.Close()
	cells, err := forgejoCells(&forgeConfig{Typ: "forgejo", URL: srv.URL, Org: "brain"}, testToken)
	if err != nil {
		t.Fatal(err)
	}
	if len(cells) != len(want) {
		t.Fatalf("%d Zellen, want %d", len(cells), len(want))
	}
	for _, c := range cells {
		if !want[c.Name] {
			t.Fatalf("Repo ohne Topic brain-cell gelistet: %s", c.Name)
		}
	}
	if strings.Join(f.pages, ",") != "1,2,3" {
		t.Fatalf("Seiten %v, want 1,2,3", f.pages)
	}
	if _, err := forgejoCells(&forgeConfig{URL: srv.URL, Org: "brain"}, "falsch"); err == nil {
		t.Fatal("falsches Token: kein Fehler")
	}
}

// Ende-zu-Ende: Login legt das Token per git credential ab (Helper store wird gesetzt), danach
// findet discoverCells über die Fake-Forge genau die Zellen mit Topic brain-cell und klont sie.
func TestLoginThenDiscoverForgejo(t *testing.T) {
	home, gitconfig := isolatedGit(t)
	src := t.TempDir()
	for _, n := range []string{"ws-a", "ws-b"} {
		d := filepath.Join(src, n)
		mustGit(t, "init", "-q", "-b", "main", d)
		os.WriteFile(filepath.Join(d, ".cell.yml"), []byte("titel: "+n+"\nstatus: aktiv\n"), 0o644)
		os.WriteFile(filepath.Join(d, "AGENTS.md"), []byte("x\n"), 0o644)
		mustGit(t, "-C", d, "add", "-A")
		mustGit(t, "-C", d, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init")
	}
	f := &fakeForgejo{org: "brain", repos: []forgeRepo{
		{Name: "brain", CloneURL: "/nicht/klonen", Topics: nil},
		{Name: "ws-a", CloneURL: filepath.Join(src, "ws-a"), Topics: []string{"brain-cell"}},
		{Name: "ws-b", CloneURL: filepath.Join(src, "ws-b"), Topics: []string{"anderes"}},
	}}
	srv := httptest.NewServer(f)
	defer srv.Close()

	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("brain\n"), 0o644)
	writeForge(t, root, "typ: forgejo\nurl: "+srv.URL+"\norg: brain\n")

	// ohne Token: Discovery meldet nur eine Warnung
	if a, g := discoverCells(root); a != nil || g != nil {
		t.Fatalf("ohne Token: %v %v", a, g)
	}
	var code int
	withStdin(t, "falsch\n", func() { code = cmdLogin([]string{"--root", root, "--no-open"}) })
	if code != 1 {
		t.Fatalf("login mit falschem Token: %d", code)
	}
	withStdin(t, testToken+"\n", func() { code = cmdLogin([]string{"--root", root, "--no-open"}) })
	if code != 0 {
		t.Fatalf("login: %d", code)
	}
	cfg, _ := os.ReadFile(gitconfig)
	if !strings.Contains(string(cfg), "helper = store") {
		t.Fatalf("credential.helper store nicht gesetzt:\n%s", cfg)
	}
	creds, _ := os.ReadFile(filepath.Join(home, ".git-credentials"))
	if !strings.Contains(string(creds), "alice:"+testToken+"@") {
		t.Fatal("Token nicht in ~/.git-credentials")
	}
	if u, p, err := gitCredential(srv.URL); err != nil || u != "alice" || p != testToken {
		t.Fatalf("credential fill: %q %v", u, err)
	}

	added, gone := discoverCells(root)
	if strings.Join(added, ",") != "ws-a" || len(gone) != 0 {
		t.Fatalf("added=%v gone=%v", added, gone)
	}
	if !isFile(filepath.Join(root, "workspaces", "ws-a", ".cell.yml")) {
		t.Fatal("ws-a nicht geklont")
	}
	if isFile(filepath.Join(root, "workspaces", "ws-a", "AGENTS.md")) {
		t.Fatal("AGENTS.md in der Zelle (Sparse-Checkout fehlt)")
	}
	if exists(filepath.Join(root, "workspaces", "ws-b")) || exists(filepath.Join(root, "workspaces", "brain")) {
		t.Fatal("Repo ohne Topic brain-cell geklont")
	}
	// zweiter Lauf: nichts Neues, nichts weg
	if a, g := discoverCells(root); len(a) != 0 || len(g) != 0 {
		t.Fatalf("2. Lauf: %v %v", a, g)
	}
	// Zelle nicht mehr freigegeben → nur gemeldet
	f.repos = f.repos[:1]
	f.repos = append(f.repos, forgeRepo{Name: "ws-c", CloneURL: "/gibt/es/nicht", Topics: []string{"brain-cell"}})
	if _, g := discoverCells(root); strings.Join(g, ",") != "ws-a" || !isDir(filepath.Join(root, "workspaces", "ws-a")) {
		t.Fatalf("gone=%v", g)
	}
}

func TestLoginKeepsExistingHelper(t *testing.T) {
	home, gitconfig := isolatedGit(t)
	store := filepath.Join(home, "eigene-creds")
	mustGit(t, "config", "--global", "credential.helper", "store --file="+store)
	srv := httptest.NewServer(&fakeForgejo{org: "brain"})
	defer srv.Close()
	var code int
	withStdin(t, testToken+"\n", func() { code = cmdLogin([]string{"--url", srv.URL + "/", "--no-open"}) })
	if code != 0 {
		t.Fatalf("login: %d", code)
	}
	cfg, _ := os.ReadFile(gitconfig)
	if strings.Count(string(cfg), "helper") != 1 {
		t.Fatalf("Helper verändert:\n%s", cfg)
	}
	if b, _ := os.ReadFile(store); !strings.Contains(string(b), testToken) {
		t.Fatal("Token nicht im vorhandenen Helper")
	}
}

func TestLoginWithoutURL(t *testing.T) {
	isolatedGit(t)
	root := t.TempDir()
	withStdin(t, testToken+"\n", func() {
		if code := cmdLogin([]string{"--root", root, "--no-open"}); code != 2 {
			t.Fatalf("ohne URL: %d", code)
		}
	})
}

func TestUpdateCheckOffForForgeBrain(t *testing.T) {
	root := t.TempDir()
	writeForge(t, root, "typ: forgejo\nurl: http://127.0.0.1:1\norg: brain\n")
	// würde sonst GitHub fragen; mit forge.yml kein Netzzugriff und keine Hinweise
	if n := updateNotices(root, true); n != nil {
		t.Fatalf("Hinweise trotz forge.yml: %v", n)
	}
}
