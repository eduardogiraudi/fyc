// Package main — ProcessInput: the core session loop logic extracted as a method.
package main

import (
	"fuckyourcli/config"
	"fuckyourcli/modes"
	"fuckyourcli/store"
	"fuckyourcli/types"
	"fuckyourcli/ui"
	"os"
	"path/filepath"
	"strings"
)

// maxHistoryEntries caps the persistent input history (one line per submitted
// prompt). Older entries are dropped from the front.
const maxHistoryEntries = 500

// historyFilePath is the persistent input-history file (~/.fyc_history).
func historyFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".fyc_history"
	}
	return filepath.Join(home, ".fyc_history")
}

// readHistoryFile returns the non-empty lines of path, capped to the most
// recent maxHistoryEntries.
func readHistoryFile(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	if len(out) > maxHistoryEntries {
		out = out[len(out)-maxHistoryEntries:]
	}
	return out
}

// dedupConsecutive collapses runs of identical consecutive entries.
func dedupConsecutive(in []string) []string {
	out := in[:0]
	for _, s := range in {
		if len(out) > 0 && out[len(out)-1] == s {
			continue
		}
		out = append(out, s)
	}
	return out
}

// loadHistoryEntries returns the persisted input history for the TUI: capped
// and with consecutive duplicates collapsed.
func loadHistoryEntries() []string {
	return dedupConsecutive(readHistoryFile(historyFilePath()))
}

// saveHistoryEntry appends a single line to the persistent history file, keeping
// the file capped at maxHistoryEntries and skipping consecutive duplicates.
func (s *ChatSession) saveHistoryEntry(entry string) {
	entry = strings.TrimRight(entry, "\r\n")
	if entry == "" {
		return
	}
	path := historyFilePath()
	entries := dedupConsecutive(readHistoryFile(path))
	if n := len(entries); n > 0 && entries[n-1] == entry {
		return
	}
	entries = append(entries, entry)
	if len(entries) > maxHistoryEntries {
		entries = entries[len(entries)-maxHistoryEntries:]
	}
	if err := store.WriteFile(path, []byte(strings.Join(entries, "\n")+"\n"), 0o600); err != nil {
		store.Notice("history: %v", err)
	}
}

// ProcessInput handles one line of user input.
// It returns false when the user wants to exit.
func (s *ChatSession) ProcessInput(input string) bool {
	if input == "exit" || input == "/exit" {
		return false
	}

	s.saveHistoryEntry(input)

	handled, modifiedInput := handleSlashCommand(input, s)
	if handled {
		if modifiedInput == "" {
			return true
		}
		input = modifiedInput
	}

	s.retryRequest = nil
	cfg := config.Get()
	mode, content := modes.DetectMode(input)
	activeMode := s.CurrentMode
	displayOneShot := false
	if mode != nil {
		if !s.webSearchEnabled() && mode.RequiresWebSearch() {
			s.Sink.WriteText(ui.InfoStyle.Render("/deep is disabled while airplane mode is enabled"))
			s.Sink.WriteNewline()
			return true
		}
		activeMode = mode
		input = content
		displayOneShot = true
	}

	// A one-shot prefix mode (/debug ..., /plan ...) applies its tool policy to
	// this turn only; the override is cleared on return so it never leaks into
	// the following turns.
	if mode != nil {
		s.turnModeOverride = activeMode
		defer func() { s.turnModeOverride = nil }()
	}

	var geminiSwapped bool
	var geminiOneShotRestore *modes.Mode
	if activeMode != nil {
		if displayOneShot {
			s.Sink.WriteText(ui.InfoStyle.Render("One-shot mode: " + activeMode.Name))
			s.Sink.WriteNewline()
		}

		if cfg.Provider == config.ProviderGemini {
			// Gemini accepts the mode as a system instruction (see
			// buildProvider); a persistent /mode is already carried by
			// s.CurrentMode, so only a one-shot override needs the temporary swap.
			if displayOneShot {
				geminiOneShotRestore = s.CurrentMode
				s.CurrentMode = activeMode
				geminiSwapped = true
			}
		} else if s.claimModeInstructions(activeMode, displayOneShot) {
			// A one-shot prefix mode is re-injected on every turn it is used; a
			// persistent /mode is injected once, so the context does not grow on
			// every turn.
			s.ChatHistory = append(s.ChatHistory, types.Message{
				Role:    "user",
				Content: "[INSTRUCTIONS]: " + activeMode.GetInstructions(s.subagentsEnabled()),
			})
		}
	}

	if strings.TrimSpace(input) == "" && activeMode != nil {
		input = "Proceed with " + activeMode.Name + " mode."
	}

	s.ChatHistory = append(s.ChatHistory, types.Message{
		Role:    "user",
		Content: input,
	})

	streamChat(s)

	if geminiSwapped {
		s.CurrentMode = geminiOneShotRestore
	}

	// Autosave at the end of every turn: a crash never loses the work.
	s.autosave()

	return true
}

func (s *ChatSession) autosave() {
	if err := saveChatSession(s); err != nil && s.Sink != nil {
		s.Sink.WriteText(ui.InfoStyle.Render("Warning: could not save chat: " + err.Error()))
		s.Sink.WriteNewline()
	}
}
