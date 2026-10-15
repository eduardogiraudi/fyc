package logger

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"fuckyourcli/store"
	"fuckyourcli/types"
)

type responseDump struct {
	Schema           int              `json:"schema"`
	SessionID        string           `json:"session_id"`
	RequestID        string           `json:"request_id"`
	Content          string           `json:"content"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []types.ToolCall `json:"tool_calls,omitempty"`
}

func (r *request) writeResponse(result *types.StreamResult, secrets []string) error {
	// Opaque reasoning state/signatures are replay credentials, not diagnostics.
	calls := make([]types.ToolCall, len(result.ToolCalls))
	copy(calls, result.ToolCalls)
	for i := range calls {
		calls[i].ThoughtSignature = ""
	}
	dump := responseDump{Schema: 1, SessionID: r.event.SessionID, RequestID: r.event.RequestID,
		Content: result.Content, ReasoningContent: result.ReasoningContent, ToolCalls: calls}
	raw, err := json.Marshal(dump)
	if err != nil {
		return err
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	data, err := json.MarshalIndent(redact(doc, secrets), "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Join(r.dir(), "responses")
	if err := store.EnsureDir(dir, store.DirPerm); err != nil {
		return err
	}
	return store.WriteFile(filepath.Join(dir, r.event.RequestID+".json"), append(data, '\n'), store.FilePerm)
}

func redact(v any, secrets []string) any {
	switch x := v.(type) {
	case map[string]any:
		for k, value := range x {
			key := strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(k))
			switch {
			case strings.Contains(key, "apikey"), strings.Contains(key, "password"), strings.Contains(key, "secret"),
				strings.Contains(key, "authorization"), strings.Contains(key, "accesstoken"), strings.Contains(key, "refreshtoken"), key == "token":
				x[k] = "[REDACTED]"
			default:
				x[k] = redact(value, secrets)
			}
		}
	case []any:
		for i := range x {
			x[i] = redact(x[i], secrets)
		}
	case string:
		for _, secret := range secrets {
			if secret != "" {
				x = strings.ReplaceAll(x, secret, "[REDACTED]")
			}
		}
		return x
	}
	return v
}
