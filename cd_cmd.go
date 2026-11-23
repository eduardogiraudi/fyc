package main

import (
	"fmt"
	"os"
	"strings"

	"fuckyourcli/config"
	"fuckyourcli/ui"
	"golang.org/x/term"

	"fuckyourcli/types"
)

// Seams the tests use to answer the startup question without a terminal. The
// /cd command itself goes through the session sink, which the existing chat
// tests already drive.
var (
	selectCD      = ui.SelectOption
	pickDirCD     = ui.PickDirectory
	multiSelectCD = ui.MultiSelectOption
	cdInteractive = interactiveTerminal
)

// handleCDCommand implements /cd. The restricted root (the folder fyc was started
// in) always stays first and relative paths keep resolving there; every folder
// added here becomes an additional allowed root, reachable through absolute
// paths, for the tools, the sandbox, the sub-agents and /undo.
func handleCDCommand(s *ChatSession, input string) {
	sink := s.commandSink()
	arg := strings.TrimSpace(strings.TrimPrefix(input, "/cd"))
	fields := strings.Fields(arg)

	if len(fields) > 0 {
		switch fields[0] {
		case "list", "ls":
			writeRootsList(s, sink)
			return
		case "clear", "reset":
			if len(s.ExtraRoots) == 0 {
				sinkLine(sink, ui.InfoStyle.Render("No additional root to remove."))
				return
			}
			s.setExtraRoots(nil)
			sinkLine(sink, ui.OkStyle.Render("Additional roots removed: only "+s.RestrictedRoot+" is allowed."))
			return
		case "remove", "rm":
			rest := strings.TrimSpace(strings.TrimPrefix(arg, fields[0]))
			if rest == "" {
				removeRootMenu(s)
				return
			}
			removeRoot(s, rest)
			return
		case "save":
			rest := strings.TrimSpace(strings.TrimPrefix(arg, "save"))
			saveWorkspaceSet(s, rest)
			return
		}
		// /cd <path> adds a folder without opening the picker.
		addRootMenu(s, arg)
		return
	}

	for {
		writeRootsList(s, sink)
		choice := sink.SelectOption("Workspace roots (/cd)", []string{
			"Add a folder…",
			"Remove a folder…",
			"Save this set for next time…",
			"Done",
		})
		switch choice {
		case 0:
			pickAndAddRoot(s)
		case 1:
			removeRootMenu(s)
		case 2:
			saveWorkspaceSet(s, "")
		default:
			return
		}
	}
}

// writeRootsList prints the allowed roots: the primary one first (relative paths
// resolve there), then every root /cd added.
func writeRootsList(s *ChatSession, sink types.OutputSink) {
	roots := s.workspaceRoots()
	if len(roots) <= 1 {
		sinkLine(sink, ui.InfoStyle.Render("Allowed roots: "+s.RestrictedRoot+" (primary)"))
		return
	}
	sinkLine(sink, ui.TitleStyle.Render(fmt.Sprintf("Allowed roots (%d):", len(roots))))
	sinkLine(sink, "  "+ui.OkStyle.Render(roots[0])+" "+ui.DimStyle.Render("(primary, relative paths resolve here)"))
	for _, root := range roots[1:] {
		sinkLine(sink, "  "+ui.OkStyle.Render(root)+" "+ui.DimStyle.Render("(absolute paths only)"))
	}
}

// pickAndAddRoot opens the directory picker and then asks whether the new set
// should be remembered.
func pickAndAddRoot(s *ChatSession) {
	sink := s.commandSink()
	start := s.RestrictedRoot
	if len(s.ExtraRoots) > 0 {
		start = s.ExtraRoots[len(s.ExtraRoots)-1]
	}
	dir, ok := pickDirCD("Allow a folder", start, s.workspaceRoots())
	if !ok || dir == "" {
		return
	}
	if msg := rootConflict(s, config.ResolveDir(dir)); msg != "" {
		sinkLine(sink, ui.InfoStyle.Render(msg))
		return
	}
	choice := sink.SelectOption("Allow "+dir+"?", []string{
		"Only for this session",
		"Save the whole set for next time",
		"Cancel",
	})
	if choice == 2 || choice < 0 {
		return
	}
	if !addRoot(s, dir) {
		return
	}
	if choice == 1 {
		saveWorkspaceSet(s, "")
	}
}

// addRootMenu is /cd <path>: it resolves the folder and asks the same
// session-or-saved question as the picker, so the two entry points behave alike.
func addRootMenu(s *ChatSession, path string) {
	sink := s.commandSink()
	resolved, err := config.AddableRoot(path)
	if err != nil {
		sinkLine(sink, ui.ErrorStyle.Render("Cannot allow that folder: "+err.Error()))
		return
	}
	if msg := rootConflict(s, resolved); msg != "" {
		sinkLine(sink, ui.InfoStyle.Render(msg))
		return
	}
	choice := sink.SelectOption("Allow "+resolved+"?", []string{
		"Only for this session",
		"Save the whole set for next time",
		"Cancel",
	})
	if choice == 2 || choice < 0 {
		return
	}
	if !addRoot(s, resolved) {
		return
	}
	if choice == 1 {
		saveWorkspaceSet(s, "")
	}
}

// addRoot validates and installs one additional root, reporting what happened.
func addRoot(s *ChatSession, dir string) bool {
	sink := s.commandSink()
	resolved, err := config.AddableRoot(dir)
	if err != nil {
		sinkLine(sink, ui.ErrorStyle.Render("Cannot allow that folder: "+err.Error()))
		return false
	}
	if msg := rootConflict(s, resolved); msg != "" {
		sinkLine(sink, ui.InfoStyle.Render(msg))
		return false
	}
	warn := ""
	for _, root := range s.workspaceRoots() {
		if strings.HasPrefix(root, resolved+string(os.PathSeparator)) {
			warn = resolved + " already contains " + root + ": this grants the model more than that folder."
			break
		}
	}
	s.setExtraRoots(append(append([]string{}, s.ExtraRoots...), resolved))
	sinkLine(sink, ui.OkStyle.Render("Allowed "+resolved+" for this session."))
	if warn != "" {
		sinkLine(sink, ui.TipStyle.Render("  note: "+warn))
	}
	if len(s.workspaceRoots()) > 2 {
		sinkLine(sink, ui.DimStyle.Render("  relative paths still resolve against "+s.RestrictedRoot+"; point at the others by absolute path."))
	}
	return true
}

// rootConflict is why root cannot be added, or "" when it can. A folder that is
// already allowed, or that lies inside an allowed one, grants nothing new:
// config.NormalizeRoots would silently drop it, so it is refused before asking
// the session-or-save question instead of reporting a permission that is not
// there.
func rootConflict(s *ChatSession, root string) string {
	for _, existing := range s.workspaceRoots() {
		if existing == root {
			return root + " is already allowed."
		}
		if strings.HasPrefix(root, existing+string(os.PathSeparator)) {
			return root + " is already inside the allowed " + existing + "."
		}
	}
	return ""
}

// removeRootMenu removes one or more additional roots.
func removeRootMenu(s *ChatSession) {
	sink := s.commandSink()
	extras := s.workspaceRoots()[1:]
	if len(extras) == 0 {
		sinkLine(sink, ui.InfoStyle.Render("No additional root to remove."))
		return
	}
	states := make([]bool, len(extras))
	picked := multiSelectCD("Remove allowed roots", extras, states)
	if picked == nil {
		return
	}
	kept := make([]string, 0, len(extras))
	for i, on := range picked {
		if !on {
			kept = append(kept, extras[i])
		}
	}
	s.setExtraRoots(kept)
	sinkLine(sink, ui.OkStyle.Render(fmt.Sprintf("Allowed roots now: %d.", len(s.workspaceRoots()))))
}

// removeRoot is the textual "/cd remove <path>" form.
func removeRoot(s *ChatSession, path string) {
	sink := s.commandSink()
	target := config.ResolveDir(path)
	kept := make([]string, 0, len(s.ExtraRoots))
	removed := false
	for _, root := range s.ExtraRoots {
		if config.ResolveDir(root) == target {
			removed = true
			continue
		}
		kept = append(kept, root)
	}
	if !removed {
		sinkLine(sink, ui.InfoStyle.Render(path+" is not an additional allowed root."))
		return
	}
	s.setExtraRoots(kept)
	sinkLine(sink, ui.OkStyle.Render("Removed "+target+"."))
}

// saveWorkspaceSet stores the current root set in config.json so a later run
// started inside any of these folders can offer it again.
func saveWorkspaceSet(s *ChatSession, name string) {
	sink := s.commandSink()
	if len(s.workspaceRoots()) < 2 {
		sinkLine(sink, ui.InfoStyle.Render("Only the primary root is allowed: add a folder first."))
		return
	}
	entry := config.UpsertSavedWorkspace(strings.TrimSpace(name), s.workspaceRoots())
	if entry.Name == "" {
		sinkLine(sink, ui.ErrorStyle.Render("Cannot save the workspace: no allowed root."))
		return
	}
	if err := config.SaveE(); err != nil {
		sinkLine(sink, ui.ErrorStyle.Render("Cannot save the configuration: "+err.Error()))
		return
	}
	sinkLine(sink, ui.OkStyle.Render(fmt.Sprintf("Saved workspace %q (%d root(s)).", entry.Name, len(entry.Roots))))
	sinkLine(sink, ui.DimStyle.Render("  starting fyc inside any of them will offer to load the set again."))
}

// startupWorkspaceOffer asks whether a remembered root set that covers the
// current folder should be loaded. It runs before the chat starts, so it uses a
// nested prompt (the alternate screen is not up yet) and does nothing when the
// terminal is not interactive.
// startupWorkspaceOffer asks whether a remembered root set that covers the
// current folder should be loaded. It runs before the chat starts, so it uses a
// nested prompt (the alternate screen is not up yet) and does nothing when the
// terminal is not interactive. dismissed reports that the user closed the
// question with q/Esc/Ctrl+C: like the chat picker and the first-run wizard, the
// caller then quits instead of starting a chat.
func startupWorkspaceOffer(s *ChatSession) (dismissed bool) {
	if s == nil || !cdInteractive() {
		return false
	}
	groups := config.WorkspacesContaining(s.RestrictedRoot)
	if len(groups) == 0 {
		return false
	}

	labels := make([]string, 0, len(groups)+1)
	for _, g := range groups {
		labels = append(labels, describeSavedWorkspace(g))
	}
	labels = append(labels, "Start with only this folder")
	pick := selectCD("A saved workspace allows this folder. Load its roots?", labels)
	switch {
	case pick == ui.SelectorCanceled || pick < 0:
		return true
	case pick >= len(groups):
		return false // "Start with only this folder"
	}
	group := groups[pick]
	// setRoots, not setExtraRoots: the chat is not running yet, and the startup
	// initialisation builds the ignore rules and the system prompt right after.
	s.setRoots(group.Roots)
	// Mark the set as the most recently used one.
	config.UpsertSavedWorkspace(group.Name, group.Roots)
	config.Save()
	return false
}

// describeSavedWorkspace renders one selector row: the name and the folders it
// allows, so the user can see what would be granted before choosing. The list is
// shortened so a workspace with many roots cannot push the row off the screen.
func describeSavedWorkspace(w config.SavedWorkspace) string {
	const maxShown = 3
	shown := w.Roots
	extra := ""
	if len(shown) > maxShown {
		extra = fmt.Sprintf(" · +%d more", len(shown)-maxShown)
		shown = shown[:maxShown]
	}
	return fmt.Sprintf("%s — %s%s", w.Name, strings.Join(shown, " · "), extra)
}

// interactiveTerminal reports whether a nested prompt can run at all.
func interactiveTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}
