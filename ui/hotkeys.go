package ui

import (
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

var ctrlOHandler struct {
	mu sync.RWMutex
	fn func() tea.Cmd
}

// SetCtrlOHandler registers the session-wide Ctrl+O handler.
func SetCtrlOHandler(fn func() tea.Cmd) {
	ctrlOHandler.mu.Lock()
	ctrlOHandler.fn = fn
	ctrlOHandler.mu.Unlock()
}

func runCtrlOHandler() tea.Cmd {
	ctrlOHandler.mu.RLock()
	fn := ctrlOHandler.fn
	ctrlOHandler.mu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn()
}
