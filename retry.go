package main

import (
	"crypto/sha256"
	"encoding/json"

	"fuckyourcli/modes"
	"fuckyourcli/types"
	"fuckyourcli/ui"
)

type retryRequest struct {
	history     [sha256.Size]byte
	mode        *modes.Mode
	oneShot     bool
	instruction string
}

// System settings may change before a retry; conversation edits must not
// silently move its sampling boundary or resurrect an earlier task.
func retryHistoryFingerprint(history []types.Message) ([sha256.Size]byte, error) {
	messages := make([]types.Message, 0, len(history))
	for _, message := range history {
		if message.Role != "system" {
			messages = append(messages, message)
		}
	}
	data, err := json.Marshal(messages)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(data), nil
}

func newRetryRequest(s *ChatSession) *retryRequest {
	history, err := retryHistoryFingerprint(s.ChatHistory)
	if err != nil {
		return nil
	}
	return &retryRequest{history: history, mode: s.activeMode(), oneShot: s.turnModeOverride != nil}
}

func retryFailedRequestWith(s *ChatSession, stream func(*ChatSession)) bool {
	checkpoint := s.retryRequest
	if checkpoint == nil {
		sinkLine(chatSink(s), ui.InfoStyle.Render("No failed provider request to retry."))
		return false
	}
	history, err := retryHistoryFingerprint(s.ChatHistory)
	if err != nil || history != checkpoint.history {
		s.retryRequest = nil
		sinkLine(chatSink(s), ui.InfoStyle.Render("Cannot retry: the conversation has changed. Send a new prompt."))
		return false
	}

	// Gemini reads CurrentMode; preserve persistent-mode bookkeeping unless
	// this retry overrides the user's newly selected mode or was one-shot.
	current, override := s.CurrentMode, s.turnModeOverride
	s.CurrentMode, s.turnModeOverride = checkpoint.mode, nil
	if checkpoint.oneShot || checkpoint.mode != current {
		s.turnModeOverride = checkpoint.mode
	}
	defer func() { s.CurrentMode, s.turnModeOverride = current, override }()
	sinkLine(chatSink(s), ui.InfoStyle.Render("Retrying the failed provider request (completed tools are not rerun)."))
	stream(s)
	return true
}
