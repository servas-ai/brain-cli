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
		err = fmt.Errorf("Timed out after %d s", sec)
	}
	return buf.String(), err
}

var forgeGitMode bool

func git(args ...string) error {
	if forgeGitMode || brainGitNeedsOIDC(args) {
		c := authenticatedGit(args)
		c.Stdout, c.Stderr = os.Stderr, os.Stderr
		return c.Run()
	}
	options, command := legacyGitParts(args)
	if command == "pull" {
		remote, err := githubPullRemote(options)
		if err != nil {
			return err
		}
		// Never let Git reinterpret branch.*.remote as a direct URL after
		// preflight. The explicit named remote is the one inspected above.
		args = append(append(append([]string{}, options...), "pull", "--ff-only"), remote)
	}
	if command == "clone" {
		empty, err := os.MkdirTemp("", "brain-github-template-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(empty)
		at := len(options) + 1
		args = append(append(append([]string{}, args[:at]...), "--template="+empty), args[at:]...)
	}
	// The GitHub exception must not follow a redirect into a Forge origin.
	c := exec.Command("git", append([]string{"-c", "http.followRedirects=false"}, args...)...)
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
		fmt.Fprintf(os.Stderr, "brain: warning: pull in %s failed (%v); continuing with the local version\n", label, err)
	}
}

func cmdSync(args []string) int {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	root := fs.String("root", "", "Brain root directory")
	noKat := fs.Bool("no-katalog", false, "do not generate _katalog/ (remove any existing catalog)")
	persp := fs.String("perspektive", "", "Markdown file with login information")
	noPull := fs.Bool("no-pull", false, "skip git pull")
	noUpd := fs.Bool("no-update-check", false, "do not check for new versions")
	noDisc := fs.Bool("no-discover", false, "do not discover new workspaces")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	r, err := findRoot(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "brain:", err)
		return 1
	}
	if !*noPull && forgeActive(r) {
		fc, e := readForge(r)
		if e != nil || fc == nil {
			return authError(fmt.Errorf("invalid Forge configuration"))
		}
		if _, e := currentAccess(fc.URL); e != nil {
			return authError(e)
		}
		oldMode := forgeGitMode
		forgeGitMode = true
		defer func() { forgeGitMode = oldMode }()
	}
	if !*noPull {
		if _, err := exec.LookPath("git"); err != nil {
			fmt.Fprintln(os.Stderr, "brain: warning: git not found; skipping pull")
		} else {
			// Check every existing repository before the first network request.
			// Removing forge.yml must not enable ambient credentials for a Forge.
			dirs := []string{r, filepath.Join(r, "personal")}
			for _, n := range listNames(filepath.Join(r, "workspaces")) {
				dirs = append(dirs, filepath.Join(r, "workspaces", n))
			}
			for _, dir := range dirs {
				if exists(filepath.Join(dir, ".git")) {
					if err := preflightBrainGit([]string{"-C", dir, "pull", "--ff-only"}); err != nil {
						return authError(err)
					}
				}
			}
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
	if err == nil && !*noUpd && !forgeActive(r) {
		notices := updateNotices(r, false)
		appendUpdatesToPerspektive(r, notices)
		for _, n := range notices {
			fmt.Println("brain sync: update available: " + n)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "brain:", err)
		return 1
	}
	catalogPrefix := ""
	if *noKat {
		catalogPrefix = "no "
	}
	fmt.Printf("brain sync: %d _kinder.md, _workspaces.md, %s_katalog, _perspektive.md\n", n, catalogPrefix)
	return 0
}

func cmdCloneCell(args []string) int {
	fs := flag.NewFlagSet("clone-cell", flag.ContinueOnError)
	name := fs.String("name", "", "Workspace slug")
	root := fs.String("root", "", "Brain root directory")
	fl, pos := splitArgs(args, map[string]bool{"name": true, "root": true})
	if err := fs.Parse(fl); err != nil {
		return 2
	}
	if len(pos) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: brain clone-cell <git-url> [--name <slug>]")
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
		fmt.Fprintf(os.Stderr, "brain: invalid slug %q\n", slug)
		return 2
	}
	r, err := findRoot(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "brain:", err)
		return 1
	}
	if forgeActive(r) {
		fc, e := readForge(r)
		if e != nil || fc == nil || !validForgeClone(fc.URL, url) {
			return authError(fmt.Errorf("clone URL is outside the configured Forge origin"))
		}
		if _, e := currentAccess(fc.URL); e != nil {
			return authError(e)
		}
		oldMode := forgeGitMode
		forgeGitMode = true
		defer func() { forgeGitMode = oldMode }()
	} else if p, e := savedProfile(); e == nil && validForgeClone(p.ForgeURL, url) {
		oldMode := forgeGitMode
		forgeGitMode = true
		defer func() { forgeGitMode = oldMode }()
	}
	if err := preflightBrainGit([]string{"clone", "--no-checkout", url}); err != nil {
		return authError(err)
	}
	target := filepath.Join(r, "workspaces", slug)
	if exists(target) {
		fmt.Fprintf(os.Stderr, "brain: %s already exists\n", target)
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
			fmt.Fprintf(os.Stderr, "brain: git %s failed: %v\n", strings.Join(s, " "), err)
			return 1
		}
	}
	fmt.Printf("brain: cell cloned to workspaces/%s (excluding AGENTS.md, CLAUDE.md, .claude/, .codex/). Next: brain sync\n", slug)
	return 0
}

var requiredFields = []string{"id", "owner", "scope", "status", "summary", "evidence", "review_by"}

func cmdCheck(args []string) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	root := fs.String("root", "", "Brain root directory")
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
			problems = append(problems, rel+": no frontmatter")
		} else {
			for _, f := range requiredFields {
				v, has := fm[f]
				if !has || (!v.isList && v.s == "") || (v.isList && len(v.l) == 0) {
					problems = append(problems, fmt.Sprintf("%s: required field %s is missing", rel, f))
				}
			}
			if fm.get("status") == "superseded" && fm.get("superseded_by") == "" {
				problems = append(problems, rel+": status superseded without superseded_by")
			}
		}
		if n := len(childNodes(dir)); n > 40 {
			problems = append(problems, fmt.Sprintf("%s: %d child nodes (maximum 40)", relSlash(r, dir), n))
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
				problems = append(problems, relSlash(r, p)+": not allowed in cells")
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
		fmt.Fprintf(os.Stderr, "brain check: %d violations in %d nodes\n", len(problems), nodes)
		return 1
	}
	fmt.Fprintf(os.Stderr, "brain check: ok (%d nodes)\n", nodes)
	return 0
}

func cmdDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	root := fs.String("root", "", "Brain root directory")
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
	warn := func(m string) { fmt.Println("WARNING " + m) }
	fail := func(m string) { fmt.Println("ERROR   " + m); fails++ }

	fmt.Println("Root: " + r)
	if !isFile(filepath.Join(r, "AGENTS.md")) {
		warn("no AGENTS.md found in the root directory")
	}
	if _, err := exec.LookPath("git"); err != nil {
		fail("git not in PATH")
	} else {
		out, _ := runTimeout(10, "git", "--version")
		ok(strings.TrimSpace(out))
		if runtime.GOOS == "windows" {
			v, _ := runTimeout(10, "git", "config", "--get", "core.longpaths")
			if strings.TrimSpace(v) == "true" {
				ok("git core.longpaths = true")
			} else {
				warn("git core.longpaths is not true; set it with: git config --global core.longpaths true")
			}
		}
	}
	if strings.Contains(strings.ToLower(r), "onedrive") {
		warn("Root is in OneDrive; Git sync conflicts are possible; consider a directory such as C:\\brain")
	} else {
		ok("Root is not in OneDrive")
	}
	for _, b := range []string{"qdrantcli", "sharepointcli"} {
		if p, err := exec.LookPath(b); err != nil {
			warn(b + " not in PATH")
		} else {
			ok(b + ": " + p)
		}
	}
	if isFile(filepath.Join(r, ".ignore")) {
		ok(".ignore exists")
	} else {
		warn(".ignore is missing (rg will also search generated files)")
	}
	if fails > 0 {
		return 1
	}
	return 0
}
