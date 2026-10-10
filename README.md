# brain

CLI für das Firmen-Brain (Landkarte + Zellen). Eine Datei, keine Abhängigkeiten, läuft auf Windows, macOS und Linux.

## Voraussetzungen

| Was | Wofür |
| --- | --- |
| Git ≥ 2.40 (Windows: Git für Windows inkl. Git Bash) | `sync` (pull) und `clone-cell` (Sparse-Checkout) |
| GitHub CLI `gh` (empfohlen) | Workspaces finden und Repos klonen, siehe Setup im brain-Repo |
| optional `qdrantcli`, `sharepointcli` im PATH | `sync` schreibt deren Sicht in `_perspektive.md` |

Betriebssysteme: Windows 10/11, macOS 12+, Linux (amd64/arm64). Keine weiteren Laufzeitabhängigkeiten (ein einzelnes Go-Programm).

## Installation

Vorgebaute Binaries: https://github.com/servas-ai/brain-cli/releases/latest (`brain_<os>_<arch>.zip` bzw. `.tar.gz`, Prüfsummen in `SHA256SUMS`). In einen Ordner im `PATH` legen; die vollständige Anleitung steht im brain-Repo: https://github.com/servas-ai/brain#setup-für-die-harness

## Befehle

- `brain update-check` – prüft, ob es neuere Versionen von Framework (`BRAIN_VERSION` im Brain) und Tools gibt; `brain sync` macht das automatisch höchstens einmal am Tag und schreibt Hinweise in `_perspektive.md` (abschalten: `--no-update-check` oder `BRAIN_NO_UPDATE_CHECK=1`).


| Befehl | Was |
| --- | --- |
| `brain sync [--root <dir>] [--no-katalog] [--perspektive <md>] [--no-pull]` | `git pull --ff-only` im Root und in jeder Zelle (`workspaces/*/`, `personal/`; Fehler nur Warnung), dann `_kinder.md`, `_workspaces.md`, `_katalog/`, `_perspektive.md` neu erzeugen |
| `brain clone-cell <git-url> [--name <slug>]` | Workspace-Zelle nach `workspaces/<slug>` klonen, per Sparse-Checkout ohne `AGENTS.md`, `CLAUDE.md`, `.claude/`, `.codex/` |
| `brain check [--root <dir>]` | Pflichtfelder im Frontmatter, max. 40 Kinder, keine Harness-Dateien in Zellen, `superseded_by` bei `superseded`; Exit 1 bei Verstößen |
| `brain doctor` | git, `core.longpaths` (Windows), OneDrive-Pfad, `qdrantcli`/`sharepointcli`, `.ignore` |
| `brain version` | Version |

Root: `--root` oder ab dem aktuellen Verzeichnis nach oben bis zur ersten `AGENTS.md`.

`_perspektive.md` nimmt `--perspektive` bzw. `<root>/.brain/perspektive.md` und hängt, falls im PATH, die Ausgabe von `qdrantcli collections` und `sharepointcli whoami` an (10 s Timeout).

## Bauen

```sh
go vet ./... && ./build.sh          # VERSION=0.1.0 Standard
```

Ergebnis in `dist/`: `brain_<version>_<os>_<arch>.zip` (Windows) bzw. `.tar.gz`, plus `SHA256SUMS`.
Nur Go-Standardbibliothek, `CGO_ENABLED=0`.

Lizenz: MIT.
