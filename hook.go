package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// brain hook stop|compact – Hooks für Harnesses (Claude Code / Codex / Grok CLI: Stop; Claude Code: SessionStart "compact").
//
// stop: Hat die Sitzung seit der letzten Prüfung recherchiert oder viel gearbeitet, aber nichts
// ins Brain geschrieben, blockiert der Hook das Beenden einmal und verlangt den Abschluss nach
// AGENTS.md („Aufgabe abschließen“). Beim zweiten Stopp (stop_hook_active) lässt er durch, damit
// es keine Schleife gibt. Höchstens einmal pro Stunde je Sitzung.

const hookReason = `PFLICHT (AGENTS.md, „Aufgabe abschließen“): Diese Sitzung hat seit der letzten Prüfung recherchiert oder gearbeitet, aber nichts ins Brain geschrieben.
Prüfe jetzt, ob dabei Wissen entstanden ist, das die Landkarte noch nicht kennt, und schreibe es:
1. Ergebnis zu einem Kunden/Projekt/Auftrag → in dessen Workspace.
2. Der Weg dorthin (Quelle, Collection, Filter, Abfrage) → Fundstelle in personal/fundstellen/ (templates/fundstelle.md), sofort ins private Repo pushen.
3. Gelöstes Tool-Problem → Fehlerbild in tools/<tool>/debugging/.
4. Neuer wiederkehrender Ablauf → templates/ablauf.md.
Geteiltes erst nach dem Sichtbarkeits-Review mit deinem Menschen einreichen (public/brain/einreichen).
Gibt es nichts Neues, sag deinem Menschen in einem Satz, warum nichts ins Brain gehört.`

const compactReminder = `Hinweis vom Brain: Der Verlauf wurde gerade zusammengefasst. Prüfe, ob aus der bisherigen Arbeit Wissen noch nicht im Brain steht (Ergebnisse → Workspace, Wege → personal/fundstellen/, Tool-Probleme → tools/<tool>/debugging/), und hol das jetzt nach (AGENTS.md, „Aufgabe abschließen“).`

type hookInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	StopHookActive bool   `json:"stop_hook_active"`
	Cwd            string `json:"cwd"`
}

type hookState struct {
	Lines     int       `json:"lines"`
	LastBlock time.Time `json:"last_block"`
}

func cmdHook(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Aufruf: brain hook stop|compact")
		return 2
	}
	switch args[0] {
	case "compact":
		fmt.Println(compactReminder)
		return 0
	case "stop":
		return hookStop()
	}
	fmt.Fprintln(os.Stderr, "brain hook: unbekannt:", args[0])
	return 2
}

func hookStatePath(session string) string {
	d, err := os.UserCacheDir()
	if err != nil || session == "" {
		return ""
	}
	return filepath.Join(d, "brain", "hook-"+strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == '.' {
			return '_'
		}
		return r
	}, session)+".json")
}

func hookStop() int {
	raw, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	var in hookInput
	json.Unmarshal(raw, &in)
	if in.StopHookActive || os.Getenv("BRAIN_NO_HOOK") == "1" {
		return 0
	}
	root, err := findRoot(in.Cwd)
	if err != nil {
		root, _ = findRoot("")
	}
	sp := hookStatePath(in.SessionID)
	var st hookState
	if sp != "" {
		if b, err := os.ReadFile(sp); err == nil {
			json.Unmarshal(b, &st)
		}
	}
	if !st.LastBlock.IsZero() && time.Since(st.LastBlock) < time.Hour {
		return 0
	}
	research, work, writes, lines := scanTranscript(in.TranscriptPath, st.Lines, root)
	save := func() {
		if sp == "" {
			return
		}
		os.MkdirAll(filepath.Dir(sp), 0o755)
		if b, err := json.Marshal(st); err == nil {
			os.WriteFile(sp, b, 0o644)
		}
	}
	if writes > 0 || (research == 0 && work < 15) {
		if writes > 0 {
			st.Lines = lines
			save()
		}
		return 0
	}
	st.Lines = lines
	st.LastBlock = time.Now()
	save()
	out, _ := json.Marshal(map[string]string{"decision": "block", "reason": hookReason})
	fmt.Println(string(out))
	return 0
}

// scanTranscript zählt ab Zeile `from` Recherche-Aufrufe, Arbeitsschritte und Schreibzugriffe ins Brain.
// Versteht das Transcript von Claude Code (type "assistant" mit tool_use) und von Grok CLI
// (updates.jsonl, session/update mit sessionUpdate "tool_call").
func scanTranscript(path string, from int, root string) (research, work, writes, lines int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, 0, from
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	rootSlash := filepath.ToSlash(root)
	count := func(name string, raw json.RawMessage) {
		work++
		var in map[string]interface{}
		json.Unmarshal(raw, &in)
		r, w := classifyTool(name, in, rootSlash)
		research += r
		writes += w
	}
	for sc.Scan() {
		lines++
		if lines <= from {
			continue
		}
		var e struct {
			Type    string `json:"type"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			Params struct {
				Update struct {
					SessionUpdate string          `json:"sessionUpdate"`
					Title         string          `json:"title"`
					RawInput      json.RawMessage `json:"rawInput"`
					Meta          struct {
						Tool struct {
							Name string `json:"name"`
						} `json:"x.ai/tool"`
					} `json:"_meta"`
				} `json:"update"`
			} `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if u := e.Params.Update; u.SessionUpdate == "tool_call" {
			name := u.Meta.Tool.Name
			if name == "" {
				name = u.Title
			}
			count(grokToolName(name, u.RawInput), u.RawInput)
			continue
		}
		if e.Type != "assistant" {
			continue
		}
		var parts []struct {
			Type  string          `json:"type"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if json.Unmarshal(e.Message.Content, &parts) != nil {
			continue
		}
		for _, p := range parts {
			if p.Type == "tool_use" {
				count(p.Name, p.Input)
			}
		}
	}
	return
}

// grokToolName bildet Grok-CLI-Werkzeuge auf die Claude-Namen ab, die classifyTool kennt.
func grokToolName(name string, raw json.RawMessage) string {
	var in struct {
		Variant string `json:"variant"`
	}
	json.Unmarshal(raw, &in)
	switch {
	case name == "run_terminal_command":
		return "Bash"
	case name == "write" || name == "search_replace" || name == "hashline_edit" || name == "multi_edit" || name == "edit_notebook":
		return "Write"
	case name == "read_file":
		return "Read"
	case name == "web_search" || name == "web_fetch" || in.Variant == "WebSearch" || in.Variant == "WebFetch":
		return "WebSearch"
	case strings.Contains(name, "__"):
		return "mcp__" + name
	}
	return name
}

// classifyTool liefert (Recherche, Schreibzugriff ins Brain) für einen Werkzeugaufruf.
func classifyTool(name string, in map[string]interface{}, rootSlash string) (research, writes int) {
	fp, _ := in["file_path"].(string)
	if fp == "" {
		fp, _ = in["target_file"].(string)
	}
	cmd, _ := in["command"].(string)
	fpSlash := filepath.ToSlash(fp)
	switch {
	case name == "Write" || name == "Edit" || name == "NotebookEdit":
		if rootSlash != "" && strings.HasPrefix(fpSlash, rootSlash) {
			writes++
		}
	case name == "WebFetch" || name == "WebSearch" || strings.HasPrefix(name, "mcp__"):
		research++
	case name == "Bash" || name == "PowerShell":
		if strings.Contains(cmd, "qdrantcli") || strings.Contains(cmd, "sharepointcli") || strings.Contains(cmd, "curl ") {
			research++
		}
		if strings.Contains(cmd, "git commit") || strings.Contains(cmd, "> personal/") || strings.Contains(cmd, "personal/fundstellen") && strings.Contains(cmd, ">") {
			writes++
		}
	case name == "Read":
		if rootSlash != "" && fp != "" && !strings.HasPrefix(fpSlash, rootSlash) {
			research++
		}
	}
	return
}
