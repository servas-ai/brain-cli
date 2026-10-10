package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func lookPath(bin string) (string, error) { return exec.LookPath(bin) }

// runTimeout führt bin mit Timeout (Sekunden) aus und liefert stdout+stderr.
func runTimeout(sec int, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(sec)*time.Second)
	defer cancel()
	var buf bytes.Buffer
	c := exec.CommandContext(ctx, bin, args...)
	c.Stdout, c.Stderr = &buf, &buf
	err := c.Run()
	if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("Timeout nach %d s", sec)
	}
	return buf.String(), err
}

func git(args ...string) error {
	c := exec.Command("git", args...)
	c.Stdout, c.Stderr = os.Stderr, os.Stderr
	c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return c.Run()
}

func gitPull(dir, label string) {
	if !exists(filepath.Join(dir, ".git")) {
		return
	}
	fmt.Fprintf(os.Stderr, "brain: git pull --ff-only (%s)\n", label)
	if err := git("-C", dir, "pull", "--ff-only"); err != nil {
		fmt.Fprintf(os.Stderr, "brain: Warnung: pull in %s fehlgeschlagen (%v), arbeite mit lokalem Stand weiter\n", label, err)
	}
}

func cmdSync(args []string) int {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	root := fs.String("root", "", "Brain-Root")
	noKat := fs.Bool("no-katalog", false, "keinen _katalog/ erzeugen (vorhandenen löschen)")
	persp := fs.String("perspektive", "", "Markdown-Datei mit Angaben zu Logins")
	noPull := fs.Bool("no-pull", false, "kein git pull")
	noUpd := fs.Bool("no-update-check", false, "nicht nach neuen Versionen suchen")
	noDisc := fs.Bool("no-discover", false, "keine neuen Workspaces suchen")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	r, err := findRoot(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "brain:", err)
		return 1
	}
	if !*noPull {
		if _, err := exec.LookPath("git"); err != nil {
			fmt.Fprintln(os.Stderr, "brain: Warnung: git nicht gefunden, überspringe pull")
		} else {
			gitPull(r, ".")
			if !*noDisc {
				reportDiscovery(discoverCells(r))
			}
			for _, n := range listNames(filepath.Join(r, "workspaces")) {
				gitPull(filepath.Join(r, "workspaces", n), "workspaces/"+n)
			}
			gitPull(filepath.Join(r, "personal"), "personal")
		}
	}
	n, err := writeKinder(r)
	if err == nil {
		err = writeWorkspaces(r)
	}
	if err == nil {
		if *noKat {
			kd := filepath.Join(r, "_katalog")
			if isDir(kd) {
				clearKatalog(kd)
				os.Remove(kd)
			}
		} else {
			err = writeKatalog(r)
		}
	}
	if err == nil {
		err = writePerspektive(r, *persp)
	}
	if err == nil && !*noUpd {
		notices := updateNotices(r, false)
		appendUpdatesToPerspektive(r, notices)
		for _, n := range notices {
			fmt.Println("brain sync: Update verfügbar: " + n)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "brain:", err)
		return 1
	}
	kein := ""
	if *noKat {
		kein = "kein "
	}
	fmt.Printf("brain sync: %d _kinder.md, _workspaces.md, %s_katalog, _perspektive.md\n", n, kein)
	return 0
}

func cmdCloneCell(args []string) int {
	fs := flag.NewFlagSet("clone-cell", flag.ContinueOnError)
	name := fs.String("name", "", "Slug des Workspaces")
	root := fs.String("root", "", "Brain-Root")
	fl, pos := splitArgs(args, map[string]bool{"name": true, "root": true})
	if err := fs.Parse(fl); err != nil {
		return 2
	}
	if len(pos) != 1 {
		fmt.Fprintln(os.Stderr, "Aufruf: brain clone-cell <git-url> [--name <slug>]")
		return 2
	}
	url := pos[0]
	slug := *name
	if slug == "" {
		u := strings.TrimRight(url, "/")
		if i := strings.LastIndexAny(u, "/:"); i >= 0 {
			u = u[i+1:]
		}
		slug = strings.TrimSuffix(u, ".git")
	}
	if slug == "" || strings.ContainsAny(slug, `/\`) || strings.HasPrefix(slug, ".") {
		fmt.Fprintf(os.Stderr, "brain: ungültiger Slug %q\n", slug)
		return 2
	}
	r, err := findRoot(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "brain:", err)
		return 1
	}
	target := filepath.Join(r, "workspaces", slug)
	if exists(target) {
		fmt.Fprintf(os.Stderr, "brain: %s existiert bereits\n", target)
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "brain:", err)
		return 1
	}
	steps := [][]string{
		{"clone", "--no-checkout", url, target},
		{"-C", target, "sparse-checkout", "set", "--no-cone", "/*", "!AGENTS.md", "!CLAUDE.md", "!.claude/", "!.codex/", "!**/AGENTS.md", "!**/CLAUDE.md"},
		{"-C", target, "checkout"},
	}
	for _, s := range steps {
		if err := git(s...); err != nil {
			fmt.Fprintf(os.Stderr, "brain: git %s fehlgeschlagen: %v\n", strings.Join(s, " "), err)
			return 1
		}
	}
	fmt.Printf("brain: Zelle geklont nach workspaces/%s (ohne AGENTS.md, CLAUDE.md, .claude/, .codex/). Danach: brain sync\n", slug)
	return 0
}

var requiredFields = []string{"id", "owner", "scope", "status", "summary", "evidence", "review_by"}

func cmdCheck(args []string) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	root := fs.String("root", "", "Brain-Root")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	r, err := findRoot(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "brain:", err)
		return 1
	}
	var problems []string
	nodes := 0
	walk(r, func(dir string, files map[string]bool) {
		if !files["README.md"] {
			return
		}
		nodes++
		rel := relSlash(r, filepath.Join(dir, "README.md"))
		fm, ok := frontmatter(filepath.Join(dir, "README.md"))
		if !ok {
			problems = append(problems, rel+": kein Frontmatter")
		} else {
			for _, f := range requiredFields {
				v, has := fm[f]
				if !has || (!v.isList && v.s == "") || (v.isList && len(v.l) == 0) {
					problems = append(problems, fmt.Sprintf("%s: Pflichtfeld %s fehlt", rel, f))
				}
			}
			if fm.get("status") == "superseded" && fm.get("superseded_by") == "" {
				problems = append(problems, rel+": status superseded ohne superseded_by")
			}
		}
		if n := len(childNodes(dir)); n > 40 {
			problems = append(problems, fmt.Sprintf("%s: %d Kind-Knoten (max. 40)", relSlash(r, dir), n))
		}
	})
	var cells []string
	for _, n := range listNames(filepath.Join(r, "workspaces")) {
		if isDir(filepath.Join(r, "workspaces", n)) {
			cells = append(cells, filepath.Join(r, "workspaces", n))
		}
	}
	if isDir(filepath.Join(r, "personal")) {
		cells = append(cells, filepath.Join(r, "personal"))
	}
	forbidden := map[string]bool{"AGENTS.md": true, "CLAUDE.md": true, ".claude": true, ".codex": true}
	for _, c := range cells {
		filepath.WalkDir(c, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && d.Name() == ".git" {
				return filepath.SkipDir
			}
			if forbidden[d.Name()] {
				problems = append(problems, relSlash(r, p)+": in Zellen nicht erlaubt")
				if d.IsDir() {
					return filepath.SkipDir
				}
			}
			return nil
		})
	}
	for _, p := range problems {
		fmt.Println(p)
	}
	if len(problems) > 0 {
		fmt.Fprintf(os.Stderr, "brain check: %d Verstöße in %d Knoten\n", len(problems), nodes)
		return 1
	}
	fmt.Fprintf(os.Stderr, "brain check: ok (%d Knoten)\n", nodes)
	return 0
}

func cmdDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	root := fs.String("root", "", "Brain-Root")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	r, err := findRoot(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "brain:", err)
		return 1
	}
	fails := 0
	ok := func(m string) { fmt.Println("ok      " + m) }
	warn := func(m string) { fmt.Println("WARNUNG " + m) }
	fail := func(m string) { fmt.Println("FEHLER  " + m); fails++ }

	fmt.Println("Root: " + r)
	if !isFile(filepath.Join(r, "AGENTS.md")) {
		warn("keine AGENTS.md im Root gefunden")
	}
	if _, err := exec.LookPath("git"); err != nil {
		fail("git nicht im PATH")
	} else {
		out, _ := runTimeout(10, "git", "--version")
		ok(strings.TrimSpace(out))
		if runtime.GOOS == "windows" {
			v, _ := runTimeout(10, "git", "config", "--get", "core.longpaths")
			if strings.TrimSpace(v) == "true" {
				ok("git core.longpaths = true")
			} else {
				warn("git core.longpaths ist nicht true; setzen mit: git config --global core.longpaths true")
			}
		}
	}
	if strings.Contains(strings.ToLower(r), "onedrive") {
		warn("Root liegt in OneDrive; Sync-Konflikte mit Git möglich, besser z. B. C:\\brain")
	} else {
		ok("Root liegt nicht in OneDrive")
	}
	for _, b := range []string{"qdrantcli", "sharepointcli"} {
		if p, err := exec.LookPath(b); err != nil {
			warn(b + " nicht im PATH")
		} else {
			ok(b + ": " + p)
		}
	}
	if isFile(filepath.Join(r, ".ignore")) {
		ok(".ignore vorhanden")
	} else {
		warn(".ignore fehlt (rg durchsucht sonst auch erzeugte Dateien)")
	}
	if fails > 0 {
		return 1
	}
	return 0
}
