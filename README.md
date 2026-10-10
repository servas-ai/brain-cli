# brain

CLI für das Firmen-Brain (Landkarte + Zellen). Eine Datei, keine Abhängigkeiten, läuft auf Windows, macOS und Linux.

## Befehle

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
