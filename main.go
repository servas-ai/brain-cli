// Command brain synchronisiert und prüft ein Firmen-Brain (Landkarte + Zellen).
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var version = "dev"

const usage = `brain – Werkzeug für das Firmen-Brain

Aufruf:
  brain sync [--root <dir>] [--no-katalog] [--perspektive <md-datei>]
  brain clone-cell <git-url> [--name <slug>] [--root <dir>]
  brain check [--root <dir>]
  brain doctor [--root <dir>]
  brain hook stop|compact Hook für Harnesses: Pflicht, Wissen ins Brain zu schreiben
  brain update-check     neuere Versionen von Framework und Tools auf GitHub prüfen
  brain login [--url <forge-url>] [--no-open]   Token für das Forge-Brain hinterlegen (stdin)
  brain version
  brain --help

Root-Default: aktuelles Verzeichnis, nach oben bis eine AGENTS.md gefunden wird.
Forge-Brain: <root>/.brain/forge.yml (typ: forgejo, url, org) statt GitHub.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var code int
	switch cmd {
	case "sync":
		code = cmdSync(args)
	case "clone-cell":
		code = cmdCloneCell(args)
	case "check":
		code = cmdCheck(args)
	case "doctor":
		code = cmdDoctor(args)
	case "hook":
		os.Exit(cmdHook(os.Args[2:]))
	case "login":
		code = cmdLogin(args)
	case "update-check":
		os.Exit(cmdUpdateCheck(os.Args[2:]))
	case "version", "--version", "-v":
		fmt.Println("brain " + version)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "brain: unbekannter Befehl %q\n\n%s", cmd, usage)
		code = 2
	}
	os.Exit(code)
}

// findRoot liefert --root (absolut) oder sucht ab cwd nach oben nach AGENTS.md.
func findRoot(flagRoot string) (string, error) {
	if flagRoot != "" {
		return filepath.Abs(flagRoot)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for d := cwd; ; {
		if isFile(filepath.Join(d, "AGENTS.md")) {
			return d, nil
		}
		p := filepath.Dir(d)
		if p == d {
			return cwd, nil
		}
		d = p
	}
}

// splitArgs trennt Flags (inkl. Werte) von Positionsargumenten, damit Flags
// auch nach Positionsargumenten stehen dürfen.
func splitArgs(args []string, valueFlags map[string]bool) (flags, pos []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if valueFlags[name] && !strings.Contains(a, "=") && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		pos = append(pos, a)
	}
	return
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
