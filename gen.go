package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	fmRe      = regexp.MustCompile(`(?s)\A---\n(.*?)\n---\n`)
	h1Re      = regexp.MustCompile(`(?m)^# (.+)$`)
	projNrRe  = regexp.MustCompile(`^\d{2}-\d{3}$`)
	normRe    = regexp.MustCompile(`(?i)^(DIN|VDI|VDE|EN|ISO|DVGW|TRGS|GEG|AMEV|ArbStättV|MLAR|MLüAR|HOAI)\b`)
	skipDirs  = map[string]bool{".git": true, ".claude": true, ".codex": true, "templates": true, "proto": true, "_katalog": true}
	katalogMx = 150
)

// val ist ein Frontmatter-/YAML-Wert: Zeichenkette oder Liste.
type val struct {
	s      string
	l      []string
	isList bool
}

func (v val) str() string {
	if v.isList {
		return strings.Join(v.l, ", ")
	}
	return v.s
}

type fields map[string]val

func (f fields) get(k string) string { return f[k].str() }

func (f fields) list(k string) []string {
	v, ok := f[k]
	if !ok {
		return nil
	}
	if v.isList {
		return v.l
	}
	if v.s == "" {
		return nil
	}
	return []string{v.s}
}

func readText(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n"), nil
}

func parseList(v string, stripQuotes bool) []string {
	var out []string
	for _, x := range strings.Split(v[1:len(v)-1], ",") {
		x = strings.TrimSpace(x)
		if x == "" {
			continue
		}
		if stripQuotes {
			x = strings.Trim(x, `'"`)
		}
		out = append(out, x)
	}
	return out
}

func isListLit(v string) bool {
	return strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]") && len(v) >= 2
}

// frontmatter parst das YAML-Frontmatter einer README.md (flach, wie der Prototyp).
// ok=false, wenn kein Frontmatter vorhanden ist.
func frontmatter(path string) (fields, bool) {
	text, err := readText(path)
	if err != nil {
		return fields{}, false
	}
	m := fmRe.FindStringSubmatch(text)
	if m == nil {
		return fields{}, false
	}
	data := fields{}
	for _, line := range strings.Split(m[1], "\n") {
		if !strings.Contains(line, ":") || strings.HasPrefix(line, " ") {
			continue
		}
		k, v, _ := strings.Cut(line, ":")
		v, _, _ = strings.Cut(v, " #")
		v = strings.TrimSpace(v)
		if isListLit(v) {
			data[strings.TrimSpace(k)] = val{l: parseList(v, true), isList: true}
		} else {
			data[strings.TrimSpace(k)] = val{s: v}
		}
	}
	if _, ok := data["titel"]; !ok {
		t := ""
		if mm := h1Re.FindStringSubmatch(text); mm != nil {
			t = strings.TrimSpace(mm[1])
		}
		data["titel"] = val{s: t}
	}
	return data, true
}

func simpleYAML(path string) fields {
	data := fields{}
	text, err := readText(path)
	if err != nil {
		return data
	}
	for _, line := range strings.Split(text, "\n") {
		line, _, _ = strings.Cut(line, "#")
		line = strings.TrimRight(line, " \t\r\n")
		if line == "" || !strings.Contains(line, ":") {
			continue
		}
		k, v, _ := strings.Cut(line, ":")
		v = strings.TrimSpace(v)
		if isListLit(v) {
			data[strings.TrimSpace(k)] = val{l: parseList(v, false), isList: true}
		} else {
			data[strings.TrimSpace(k)] = val{s: v}
		}
	}
	return data
}

func isNode(d string) bool { return isFile(filepath.Join(d, "README.md")) }

func skipName(name string) bool {
	return skipDirs[name] || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

func listNames(d string) []string {
	ents, err := os.ReadDir(d)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func childNodes(d string) []string {
	var out []string
	for _, name := range listNames(d) {
		p := filepath.Join(d, name)
		if skipName(name) {
			continue
		}
		if isDir(p) && isNode(p) {
			out = append(out, p)
		}
	}
	return out
}

// walk besucht top-down alle Verzeichnisse unter root (ohne übersprungene),
// wie os.walk mit gefilterten dirnames. Symlinks auf Verzeichnisse werden nicht verfolgt.
func walk(root string, fn func(dir string, files map[string]bool)) {
	ents, err := os.ReadDir(root)
	if err != nil {
		return
	}
	files := map[string]bool{}
	var dirs []string
	for _, e := range ents {
		if e.IsDir() {
			if !skipName(e.Name()) {
				dirs = append(dirs, e.Name())
			}
		} else {
			files[e.Name()] = true
		}
	}
	fn(root, files)
	sort.Strings(dirs)
	for _, d := range dirs {
		walk(filepath.Join(root, d), fn)
	}
}

func relSlash(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(r)
}

func writeKinder(root string) (int, error) {
	count := 0
	var firstErr error
	walk(root, func(dir string, files map[string]bool) {
		if !isNode(dir) {
			return
		}
		kids := childNodes(dir)
		if len(kids) == 0 {
			return
		}
		var b strings.Builder
		fmt.Fprintf(&b, "# Kinder von `%s`\n\nLokal erzeugt, nicht ändern.\n\n| Knoten | Worum es geht |\n| --- | --- |\n", relSlash(root, dir))
		rows := make([]string, 0, len(kids))
		for _, k := range kids {
			fm, _ := frontmatter(filepath.Join(k, "README.md"))
			name := filepath.Base(k)
			summary := fm.get("summary")
			if summary == "" {
				summary = "(kein summary)"
			}
			mark := ""
			if fm.get("status") == "superseded" {
				sb := "?"
				if _, ok := fm["superseded_by"]; ok {
					sb = fm.get("superseded_by")
				}
				mark = fmt.Sprintf(" **ÜBERHOLT** → `%s`.", sb)
			}
			more := ""
			if n := len(childNodes(k)); n > 0 {
				more = fmt.Sprintf(" (%d Kinder)", n)
			}
			rows = append(rows, fmt.Sprintf("| [%s](%s/README.md)%s | %s%s |", name, name, more, summary, mark))
		}
		b.WriteString(strings.Join(rows, "\n") + "\n")
		if err := os.WriteFile(filepath.Join(dir, "_kinder.md"), []byte(b.String()), 0o644); err != nil && firstErr == nil {
			firstErr = err
		}
		count++
	})
	return count, firstErr
}

func cellNames(root string) []string {
	ws := filepath.Join(root, "workspaces")
	var names []string
	for _, n := range listNames(ws) {
		if isFile(filepath.Join(ws, n, ".cell.yml")) {
			names = append(names, n)
		}
	}
	return names
}

func writeWorkspaces(root string) error {
	rows := map[string][]string{}
	for _, name := range cellNames(root) {
		c := simpleYAML(filepath.Join(root, "workspaces", name, ".cell.yml"))
		st := "aktiv"
		if v, ok := c["status"]; ok {
			st = v.str()
		}
		rows[st] = append(rows[st], fmt.Sprintf("| [%s](workspaces/%s/README.md) | %s | %s | %s | %s | %s |",
			name, name, c.get("titel"), c.get("art"), c.get("phase"), c.get("keys"), c.get("satz")))
	}
	var b strings.Builder
	b.WriteString("# Deine Workspaces\n\nLokal erzeugt aus den Zellen, die dein GitHub-Login lesen darf. Nicht ändern.\n")
	for _, st := range []string{"aktiv", "ruhend", "archiviert"} {
		if len(rows[st]) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n## %s\n\n| Workspace | Titel | Art | Phase | Schlüssel | Worum es geht |\n| --- | --- | --- | --- | --- | --- |\n", st)
		b.WriteString(strings.Join(rows[st], "\n") + "\n")
	}
	return os.WriteFile(filepath.Join(root, "_workspaces.md"), []byte(b.String()), 0o644)
}

func keyKind(k string) string {
	if projNrRe.MatchString(k) {
		return "projektnummern"
	}
	if normRe.MatchString(k) {
		return "normen-und-regelwerke"
	}
	return "begriffe-und-kuerzel"
}

type entry struct{ key, path, summary, status string }

func clearKatalog(kd string) {
	for _, n := range listNames(kd) {
		os.Remove(filepath.Join(kd, n))
	}
}

func writeKatalog(root string) error {
	entries := map[string][]entry{}
	walk(root, func(dir string, files map[string]bool) {
		rel := relSlash(root, dir)
		if files["README.md"] {
			fm, _ := frontmatter(filepath.Join(dir, "README.md"))
			for _, k := range fm.list("keys") {
				kk := keyKind(k)
				entries[kk] = append(entries[kk], entry{k, rel + "/README.md", fm.get("summary"), fm.get("status")})
			}
		}
		if files[".cell.yml"] {
			c := simpleYAML(filepath.Join(dir, ".cell.yml"))
			if v := c["keys"]; v.isList {
				for _, k := range v.l {
					kk := keyKind(k)
					entries[kk] = append(entries[kk], entry{k, rel + "/README.md", c.get("satz"), c.get("status")})
				}
			}
		}
	})
	kd := filepath.Join(root, "_katalog")
	if err := os.MkdirAll(kd, 0o755); err != nil {
		return err
	}
	clearKatalog(kd)
	// große Arten nach Anfangsbuchstabe aufteilen
	kinds := make([]string, 0, len(entries))
	for k := range entries {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		items := entries[kind]
		if len(items) <= katalogMx {
			continue
		}
		delete(entries, kind)
		for _, it := range items {
			r, _ := utf8.DecodeRuneInString(it.key)
			grp := "0-9"
			if unicode.IsLetter(r) {
				grp = string(unicode.ToLower(r))
			}
			entries[kind+"-"+grp] = append(entries[kind+"-"+grp], it)
		}
	}
	kinds = kinds[:0]
	for k := range entries {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	var b strings.Builder
	b.WriteString("# Katalog\n\nLokal erzeugt aus den `keys` aller Knoten, die du sehen darfst. Nicht ändern. " +
		"Wähle die Datei nach der Art deines Schlüssels:\n\n")
	for _, kind := range kinds {
		fmt.Fprintf(&b, "- [%s](%s.md): %d Einträge\n", kind, kind, len(entries[kind]))
	}
	if err := os.WriteFile(filepath.Join(kd, "README.md"), []byte(b.String()), 0o644); err != nil {
		return err
	}
	for _, kind := range kinds {
		items := append([]entry(nil), entries[kind]...)
		sort.SliceStable(items, func(i, j int) bool { return strings.ToLower(items[i].key) < strings.ToLower(items[j].key) })
		var f strings.Builder
		fmt.Fprintf(&f, "# Katalog: %s\n\n| Schlüssel | Knoten | Worum es geht |\n| --- | --- | --- |\n", kind)
		for _, it := range items {
			mark := ""
			if it.status == "superseded" {
				mark = " **ÜBERHOLT**"
			}
			fmt.Fprintf(&f, "| %s | [%s](../%s) | %s%s |\n", it.key, it.path, it.path, it.summary, mark)
		}
		if err := os.WriteFile(filepath.Join(kd, kind+".md"), []byte(f.String()), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func writePerspektive(root, src string) error {
	var b strings.Builder
	b.WriteString("# Deine Perspektive\n\nLokal erzeugt aus deinen Logins. Nicht ändern. " +
		"Was hier fehlt, sieht dein Mensch nicht; versuch es nicht.\n\n")
	if src == "" {
		if d := filepath.Join(root, ".brain", "perspektive.md"); isFile(d) {
			src = d
		}
	}
	if src != "" && isFile(src) {
		t, err := readText(src)
		if err != nil {
			return err
		}
		b.WriteString(t)
	} else {
		b.WriteString("(keine Angaben zu Logins)\n")
	}
	names := cellNames(root)
	personal := "fehlt"
	if isDir(filepath.Join(root, "personal")) {
		personal = "vorhanden"
	}
	fmt.Fprintf(&b, "\n## GitHub-Zellen\n\n- personal/: %s\n- Workspaces (%d): siehe `_workspaces.md`\n", personal, len(names))
	for _, n := range names {
		fmt.Fprintf(&b, "  - `workspaces/%s`\n", n)
	}
	for _, t := range []struct{ bin, arg, title string }{
		{"qdrantcli", "collections", "Qdrant-Collections (`qdrantcli collections`)"},
		{"sharepointcli", "whoami", "SharePoint-Login (`sharepointcli whoami`)"},
	} {
		if _, err := lookPath(t.bin); err != nil {
			continue
		}
		out, err := runTimeout(10, t.bin, t.arg)
		fmt.Fprintf(&b, "\n## %s\n\n", t.title)
		if s := strings.TrimRight(out, "\n"); s != "" {
			b.WriteString("```\n" + s + "\n```\n")
		}
		if err != nil {
			fmt.Fprintf(&b, "Fehler: %v\n", err)
		}
	}
	return os.WriteFile(filepath.Join(root, "_perspektive.md"), []byte(b.String()), 0o644)
}
