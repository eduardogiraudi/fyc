package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"fuckyourcli/config"
	"fuckyourcli/types"
	"fuckyourcli/ui"

	"github.com/sergi/go-diff/diffmatchpatch"
)

// stdinMu serializes all interactive stdin prompts across goroutines.
var stdinMu sync.Mutex

// permissionPersistence controls whether "Allow always" answers are written to
// the shared configuration file. The interactive CLI keeps it enabled (that is
// how a choice survives a restart); the AG-UI server disables it, because there
// "remember" decisions are per-thread and live in the interaction broker
var permissionPersistence atomic.Bool

func init() { permissionPersistence.Store(true) }

// SetPermissionPersistence enables or disables global permission persistence.
func SetPermissionPersistence(enabled bool) { permissionPersistence.Store(enabled) }

func permissionPersistenceEnabled() bool { return permissionPersistence.Load() }

// persistAllowFileWrite records the "always allow file writes" grant.
func persistAllowFileWrite() {
	if !permissionPersistenceEnabled() {
		return
	}
	config.SetConfigAllowFileWrite(true, false)
	config.Save()
}

// persistAllowComputerUse records the "always allow computer use" grant.
func persistAllowComputerUse() {
	if !permissionPersistenceEnabled() {
		return
	}
	config.SetConfigAllowComputerUse(true)
	config.Save()
}

// PermissionResult represents the result of a permission prompt.
type PermissionResult int

const (
	PermissionAllow              PermissionResult = iota // user selected Allow / Confirm / Run / Apply
	PermissionDeny                                       // user selected Deny / Cancel
	PermissionDenyWithSuggestion                         // user selected "Deny with suggestion" and provided text
)

// askPermission shows a selector with Allow, Deny, and Deny with suggestion.
// Returns (result, suggestionText) where suggestionText is non-empty only for
// PermissionDenyWithSuggestion.
func askPermission(ctx context.Context, title string, options []string) (PermissionResult, string) {
	return askPermissionDetailed(ctx, title, "", options, "", nil)
}

// askPermissionDetailed is askPermission with an optional "See details" option:
// when detailsTitle/details are non-empty the interactor offers an extra option
// that opens a scrollable details viewer and then re-shows the prompt. It is
// used where the confirmation covers more than the one-line title can show (a
// long shell command, the sandbox envelope, an exact git invocation). shortTitle,
// when non-empty, is what the inline TUI selector renders instead of title.
// details carries typed lines (types.DetailLine): Text is the payload and Kind
// drives the viewer's colouring, so the shell command section is highlighted and
// the sandbox envelope is styled instead of being dumped as plain text.
func askPermissionDetailed(ctx context.Context, title, shortTitle string, options []string, detailsTitle string, details []types.DetailLine) (PermissionResult, string) {
	if config.Get().NoAsk {
		return PermissionAllow, ""
	}

	fullOptions := make([]string, len(options)+1)
	copy(fullOptions, options)
	fullOptions[len(options)] = "Deny with suggestion"

	req := InteractionRequest{
		Kind:            KindApproval,
		Title:           title,
		ShortTitle:      shortTitle,
		Options:         fullOptions,
		AllowIndex:      0,
		DenyIndex:       1,
		SuggestionIndex: len(options),
	}
	if len(details) > 0 {
		req.SeeDetailsLabel = "See details"
		req.DetailsTitle = detailsTitle
		req.Details = details
	}
	resp, err := interact(ctx, req)
	if err != nil {
		return PermissionDeny, ""
	}

	switch resp.Choice {
	case 0:
		return PermissionAllow, ""
	case req.SuggestionIndex:
		return PermissionDenyWithSuggestion, resp.Text
	default:
		return PermissionDeny, ""
	}
}

// ── Diff viewer helpers ──────────────────────────────────────────────────────

// goDiffToDiffLines converts go-diff diffs into ui.DiffLine slice.
// Assumes diffs are line-level (each block contains complete lines with \n separators).
func goDiffToDiffLines(diffs []diffmatchpatch.Diff) []ui.DiffLine {
	var lines []ui.DiffLine
	oldLine := 1
	newLine := 1
	for _, d := range diffs {
		var kind ui.DiffLineKind
		switch d.Type {
		case diffmatchpatch.DiffInsert:
			kind = ui.DiffAdded
		case diffmatchpatch.DiffDelete:
			kind = ui.DiffRemoved
		default:
			kind = ui.DiffContext
		}
		text := d.Text
		texts := strings.Split(text, "\n")
		n := len(texts)
		// If text ends with \n, discard the trailing empty string
		if text != "" && strings.HasSuffix(text, "\n") {
			n--
		}
		for _, t := range texts[:n] {
			var lineNum int
			switch d.Type {
			case diffmatchpatch.DiffEqual:
				lineNum = oldLine // old == new for context
				oldLine++
				newLine++
			case diffmatchpatch.DiffDelete:
				lineNum = oldLine
				oldLine++
			case diffmatchpatch.DiffInsert:
				lineNum = newLine
				newLine++
			}
			lines = append(lines, ui.DiffLine{
				Kind:    kind,
				Content: t,
				LineNum: lineNum,
			})
		}
	}
	return lines
}

// computeDiffStats counts added/removed lines from a ui.DiffLine slice.
func computeDiffStats(lines []ui.DiffLine) (added, removed int) {
	for _, l := range lines {
		switch l.Kind {
		case ui.DiffAdded:
			added++
		case ui.DiffRemoved:
			removed++
		}
	}
	return
}

// formatDiffStats returns a styled string like "+5 -3".
func formatDiffStats(added, removed int) string {
	var parts []string
	if added > 0 {
		parts = append(parts, ui.AddStyle.Render(fmt.Sprintf("+%d", added)))
	}
	if removed > 0 {
		parts = append(parts, ui.DeleteStyle.Render(fmt.Sprintf("-%d", removed)))
	}
	if len(parts) == 0 {
		return ui.InfoStyle.Render("no changes")
	}
	return strings.Join(parts, " ")
}

// askDiffWithViewer shows a permission prompt with an additional "See changes"
// option. When selected, it opens the diff viewer TUI. After dismissal the
// prompt reappears.
//
//	title: styled file path (e.g. "File: /path")
//	stats: formatted stats string (e.g. "+5 -3")
//	diffLines: lines for the viewer
//	options: first two = confirm & cancel (e.g. ["Apply", "Cancel"])
func askDiffWithViewer(ctx context.Context, title, stats, path string, diffLines []ui.DiffLine, options []string) (PermissionResult, string) {
	fullOptions := make([]string, 0, len(options)+1)
	fullOptions = append(fullOptions, options...)
	fullOptions = append(fullOptions, "Deny with suggestion")

	req := InteractionRequest{
		Kind:            KindApproval,
		Title:           title + "\n" + stats + "\n",
		StyledTitle:     true,
		Options:         fullOptions,
		AllowIndex:      0,
		DenyIndex:       1,
		SuggestionIndex: len(options),
		SeeChangesLabel: "See changes",
		Diff:            diffLines,
		DiffStats:       stats,
		DiffTitle:       title,
		DiffFilename:    path,
	}
	resp, err := interact(ctx, req)
	if err != nil {
		return PermissionDeny, ""
	}

	switch {
	case resp.Choice == 0: // Apply / Confirm
		return PermissionAllow, ""
	case resp.Choice == req.SuggestionIndex:
		return PermissionDenyWithSuggestion, resp.Text
	default:
		return PermissionDeny, ""
	}
}

// askUpdatePermission shows a diff between oldStr and newStr, asking the user to confirm.
func askUpdatePermission(ctx context.Context, path, oldStr, newStr string) (bool, string) {
	if config.Get().NoAsk {
		return true, ""
	}

	dmp := diffmatchpatch.New()

	// Line-level diff: convert lines to rune IDs, diff at rune level, then convert back.
	runes1, runes2, lineArray := dmp.DiffLinesToRunes(oldStr, newStr)
	diffs := dmp.DiffMainRunes(runes1, runes2, false)
	diffs = dmp.DiffCharsToLines(diffs, lineArray)
	diffs = dmp.DiffCleanupSemantic(diffs)

	// Build DiffLine slice and compute stats
	diffLines := goDiffToDiffLines(diffs)
	addedLines, removedLines := computeDiffStats(diffLines)
	stats := formatDiffStats(addedLines, removedLines)

	title := ui.FileStyle.Render("File: " + path)

	result, suggestion := askDiffWithViewer(ctx, title, stats, path, diffLines, []string{"Apply", "Cancel"})
	switch result {
	case PermissionAllow:
		return true, ""
	case PermissionDenyWithSuggestion:
		return false, suggestion
	default:
		return false, ""
	}
}

// askWritePermission asks the user to allow writing to a file, with an option to remember the choice.
func askWritePermission(ctx context.Context, path string) (bool, string) {
	cfg := config.Get()

	if cfg.NoAsk {
		return true, ""
	}

	if cfg.AllowFileWrite && !cfg.AskFileWriteEach {
		return true, ""
	}

	fullOptions := []string{"Allow", "Deny", "Allow always", "Deny with suggestion"}
	denySuggIdx := len(fullOptions) - 1
	req := InteractionRequest{
		Kind:            KindApproval,
		Title:           fmt.Sprintf("Write file: %s", path),
		Options:         fullOptions,
		AllowIndex:      0,
		DenyIndex:       1,
		SuggestionIndex: denySuggIdx,
	}
	resp, err := interact(ctx, req)
	if err != nil {
		return false, ""
	}

	switch resp.Choice {
	case 0: // Allow
		if resp.Remember {
			// The AG-UI broker already recorded the decision for this thread:
			// nothing may be written to the shared configuration.
			return true, ""
		}
		// The follow-up prompt only makes sense when the grant can be persisted:
		// in server mode (AG-UI) persistence is off, so asking would be noise the
		// client has to answer for nothing (and, with level-2 interrupts, would
		// cost an extra run round trip).
		if permissionPersistenceEnabled() && askRememberChoice(ctx) {
			persistAllowFileWrite()
		}
		return true, ""
	case 2: // Allow always
		persistAllowFileWrite()
		return true, ""
	case denySuggIdx: // Deny with suggestion
		return false, resp.Text
	default: // Deny (index 1)
		return false, ""
	}
}

// askRememberChoice is the secondary "Remember choice?" prompt shown after a
// plain "Allow". It is only reached when permission persistence is enabled: the
// AG-UI server (and the level-2 interrupt lifecycle) never persists a grant, and
// the "remember" decision travels in the client's answer instead.
// In headless mode it deterministically answers "No" so no configuration is
// silently persisted by the server.
func askRememberChoice(ctx context.Context) bool {
	resp, err := interact(ctx, InteractionRequest{
		Kind:            KindApproval,
		Title:           "Remember choice?",
		Options:         []string{"No", "Yes"},
		AllowIndex:      0,
		DenyIndex:       0,
		SuggestionIndex: -1,
	})
	if err != nil {
		return false
	}
	return resp.Choice == 1
}

// askDeletePermission asks the user to confirm deletion of a file or directory.
func askDeletePermission(ctx context.Context, path string) (bool, string) {
	res, suggestion := askPermission(ctx,
		fmt.Sprintf("Delete: %s", path),
		[]string{"Confirm", "Cancel"},
	)
	if res == PermissionAllow {
		return true, ""
	}
	if res == PermissionDenyWithSuggestion {
		return false, suggestion
	}
	return false, ""
}

// askExecPermission asks the user to confirm execution of a shell command.
// details carries the full context shown by the "See details" option (the whole
// command, the sandbox envelope). Title carries the full command — authoritative,
// it is what the viewers and the AG-UI wire use — while the TUI selector renders
// the compact ShortTitle so it never grows taller than the terminal.
func askExecPermission(ctx context.Context, cmd string, details []types.DetailLine) (bool, string) {
	res, suggestion := askPermissionDetailed(ctx,
		"Execute:\n"+cmd,                 // full, authoritative (wire + viewers)
		"Execute:\n"+commandPreview(cmd), // compact, for the inline TUI selector
		[]string{"Run", "Cancel"},
		"Execution details",
		details,
	)
	if res == PermissionAllow {
		return true, ""
	}
	if res == PermissionDenyWithSuggestion {
		return false, suggestion
	}
	return false, ""
}

// commandPreview returns a short title preview of a shell command: the first
// line, capped in width, plus a note when the command spans further lines. The
// full command is shown by the "See details" viewer, which is scrollable and
// runs in the alternate screen — unlike the inline selector, which truncated a
// tall command's head off the top of the terminal.
func commandPreview(cmd string) string {
	lines := strings.Split(strings.TrimRight(cmd, "\n"), "\n")
	first := strings.TrimSpace(lines[0])
	const maxRunes = 120
	if r := []rune(first); len(r) > maxRunes {
		first = string(r[:maxRunes]) + "…"
	}
	if len(lines) > 1 {
		return fmt.Sprintf("%s\n(+%d more line(s) — see details for the full command)", first, len(lines)-1)
	}
	return first
}

// askCopyPermission asks the user to allow copying a file/directory.
func askCopyPermission(ctx context.Context, source, destination string) (bool, string) {
	res, suggestion := askPermission(ctx,
		fmt.Sprintf("Copy:\n  from: %s\n  to:   %s", source, destination),
		[]string{"Allow", "Cancel"},
	)
	if res == PermissionAllow {
		return true, ""
	}
	if res == PermissionDenyWithSuggestion {
		return false, suggestion
	}
	return false, ""
}

// askRenamePermission asks the user to confirm renaming a file/directory.
func askRenamePermission(ctx context.Context, oldPath, newPath string) (bool, string) {
	res, suggestion := askPermission(ctx,
		fmt.Sprintf("Rename:\n  from: %s\n  to:   %s", oldPath, newPath),
		[]string{"Rename", "Cancel"},
	)
	if res == PermissionAllow {
		return true, ""
	}
	if res == PermissionDenyWithSuggestion {
		return false, suggestion
	}
	return false, ""
}

// askMovePermission asks the user to confirm moving a file/directory.
func askMovePermission(ctx context.Context, source, destination string) (bool, string) {
	res, suggestion := askPermission(ctx,
		fmt.Sprintf("Move:\n  from: %s\n  to:   %s", source, destination),
		[]string{"Move", "Cancel"},
	)
	if res == PermissionAllow {
		return true, ""
	}
	if res == PermissionDenyWithSuggestion {
		return false, suggestion
	}
	return false, ""
}

// askMakeDirPermission asks the user to confirm creation of a directory.
func askMakeDirPermission(ctx context.Context, path string) (bool, string) {
	res, suggestion := askPermission(ctx,
		fmt.Sprintf("Create directory: %s", path),
		[]string{"Confirm", "Cancel"},
	)
	if res == PermissionAllow {
		return true, ""
	}
	if res == PermissionDenyWithSuggestion {
		return false, suggestion
	}
	return false, ""
}

// askComputerUsePermission asks the user to allow a computer-use action
// (screenshot, mouse or keyboard). Mirrors askWritePermission: "Allow" can be
// remembered for the session/config, "Allow always" persists the choice.
func askComputerUsePermission(ctx context.Context, actionDesc string) (bool, string) {
	cfg := config.Get()

	if cfg.NoAsk {
		return true, ""
	}
	if cfg.AllowComputerUse {
		return true, ""
	}

	fullOptions := []string{"Allow", "Deny", "Allow always", "Deny with suggestion"}
	denySuggIdx := len(fullOptions) - 1
	req := InteractionRequest{
		Kind:            KindApproval,
		Title:           "Computer control:\n" + actionDesc,
		Options:         fullOptions,
		AllowIndex:      0,
		DenyIndex:       1,
		SuggestionIndex: denySuggIdx,
	}
	resp, err := interact(ctx, req)
	if err != nil {
		return false, ""
	}

	switch resp.Choice {
	case 0: // Allow
		if resp.Remember {
			return true, ""
		}
		if permissionPersistenceEnabled() && askRememberChoice(ctx) {
			persistAllowComputerUse()
		}
		return true, ""
	case 2: // Allow always
		persistAllowComputerUse()
		return true, ""
	case denySuggIdx: // Deny with suggestion
		return false, resp.Text
	default: // Deny (index 1)
		return false, ""
	}
}
