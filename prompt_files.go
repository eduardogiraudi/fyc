package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"fuckyourcli/config"
	"fuckyourcli/internal/mentions"
	"fuckyourcli/tools"
	"fuckyourcli/types"
)

const maxPromptFiles = 8
const maxPromptTextBytes = 512 << 10
const maxPromptImageBytes = 24 << 20
const maxPromptFileTokens = 32_000

func preparePromptFiles(input string, ws tools.Workspace, ignore *tools.IgnoreManager) (types.Message, []string, error) {
	msg := types.Message{Role: "user", Content: input}
	var names []string
	seen := make(map[string]bool)
	textBytes, imageBytes := 0, 0
	for _, ref := range mentions.Scan(input, false) {
		if len([]rune(ref.Path)) > mentions.MaxPathRunes {
			return types.Message{}, nil, fmt.Errorf("file path exceeds %d characters", mentions.MaxPathRunes)
		}
		path := filepath.Clean(filepath.FromSlash(ref.Path))
		if seen[path] {
			continue
		}
		seen[path] = true
		if len(names) == maxPromptFiles {
			return types.Message{}, nil, fmt.Errorf("at most %d files per prompt", maxPromptFiles)
		}
		result, err := tools.PromptFileIn(path, ws, ignore)
		if err != nil {
			return types.Message{}, nil, fmt.Errorf("%s: %w", ref.Path, err)
		}
		if len(result.Images) > 0 {
			for _, img := range result.Images {
				imageBytes += len(img.Data)
			}
			if imageBytes > maxPromptImageBytes {
				return types.Message{}, nil, fmt.Errorf("images exceed 24 MiB encoded per prompt")
			}
			msg.Images = append(msg.Images, result.Images...)
		} else {
			textBytes += len(result.Text)
			if textBytes > maxPromptTextBytes {
				return types.Message{}, nil, fmt.Errorf("attached text exceeds 512 KiB per prompt")
			}
			msg.Content += "\n\n[Attached file " + strconv.Quote(ref.Path) + "]\n" + result.Text + "\n[End attached file]"
		}
		if estimateMessageTokens(msg)-estimateTokens(input) > maxPromptFileTokens {
			return types.Message{}, nil, fmt.Errorf("attachments exceed the estimated %d-token budget", maxPromptFileTokens)
		}
		names = append(names, mentions.Tag(ref.Path))
	}
	return msg, names, nil
}

func (s *ChatSession) attachPromptFiles(input, fileInput string) (types.Message, bool) {
	// Only user-authored mentions attach files, never expanded skill instructions.
	msg, names, err := preparePromptFiles(fileInput, s.workspace(), s.GlobalIgnore)
	if err == nil {
		msg.Content = input + strings.TrimPrefix(msg.Content, fileInput)
	}
	// Reject rather than silently pruning images from the just-submitted prompt.
	if err == nil && len(msg.Images) > config.EffectiveMaxMedia() {
		err = fmt.Errorf("at most %d images with the current max_media setting", config.EffectiveMaxMedia())
	}
	if err != nil {
		s.Sink.WriteText("Attachment error: " + err.Error())
		s.Sink.WriteNewline()
		return types.Message{}, false
	}
	if len(names) > 0 {
		s.Sink.WriteText("Attached: " + strings.Join(names, ", "))
		s.Sink.WriteNewline()
	}
	return msg, true
}
