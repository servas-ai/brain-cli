// Command brain synchronisiert und prüft ein Firmen-Brain (Landkarte + Zellen).
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var version = "dev"

const usage = `brain - company Brain toolkit

Usage:
  brain sync [--root <dir>] [--no-katalog] [--perspektive <markdown-file>]
  brain clone-cell <git-url> [--name <slug>] [--root <dir>]
  brain check [--root <dir>]
  brain doctor [--root <dir>]
  brain hook stop|compact
  brain update-check
  brain auth configure|status|logout
  brain login [--url <forge-origin>] [--root <dir>]
  brain git -- <git arguments>
  brain propose --repo owner/repo --branch proposal --title 'Title'
  brain guide
  brain version
  brain --help

Root: current directory, searched upwards for AGENTS.md.
Forge: <root>/.brain/forge.yml (typ: forgejo, url, org).
Human login requires a registered Zitadel native client and KIRA API/Git adapter.
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
	case "auth":
		code = cmdAuth(args)
	case "git":
		code = cmdGit(args)
	case "git-credential":
		code = cmdGitCredential(args)
	case "propose":
		code = cmdPropose(args)
	case "guide":
		fmt.Print(authGuide)
	case "update-check":
		os.Exit(cmdUpdateCheck(os.Args[2:]))
	case "version", "--version", "-v":
		fmt.Println("brain " + version)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "brain: unknown command %q\n\n%s", cmd, usage)
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
