package providers

import (
	"encoding/json"
	"strings"

	"fuckyourcli/types"
)

type diffCraftTracker struct {
	sink    OutputSink
	viewing bool
}

func newDiffCraftTracker(sink OutputSink) *diffCraftTracker {
	if sink == nil {
		return nil
	}
	return &diffCraftTracker{sink: sink}
}

func (t *diffCraftTracker) update(name, rawArgs string) {
	if t == nil || name != "replace_anchors" {
		return
	}
	if !t.viewing {
		t.sink.StopSpinner()
		t.viewing = true
	}
	add, del := diffCraftStats(rawArgs)
	if ds, ok := t.sink.(types.DiffCraftSink); ok {
		ds.WriteDiffCraftFor(diffCraftLabel(rawArgs), add, del)
		return
	}
	t.sink.WriteDiffCraft(add, del)
}

// diffCraftLabel builds the live label shown while replace_anchors streams its
// arguments. The file path may not have arrived yet, so it falls back to a
// generic, grammatical phrase.
func diffCraftLabel(rawArgs string) string {
	if path := strings.TrimSpace(extractJSONStringValue(rawArgs, "path")); path != "" {
		return "patching file " + path
	}
	return "patching file"
}

func (t *diffCraftTracker) finish() {
	if t == nil || !t.viewing {
		return
	}
	t.sink.FinishDiffCraft()
	t.viewing = false
}

func diffCraftStats(rawArgs string) (int, int) {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return countReplacementLines(extractJSONStringValue(rawArgs, "replacement")), 0
	}
	replacement, _ := args["replacement"].(string)
	add := countReplacementLines(replacement)
	del := 0
	if strings.TrimSpace(replacement) == "" {
		del = 1
	} else if start, _ := args["start_anchor"].(string); start != "" {
		if end, _ := args["end_anchor"].(string); end != "" && end != start {
			del = 2
		} else {
			del = 1
		}
	}
	return add, del
}

func countReplacementLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func extractJSONStringValue(raw, key string) string {
	needle := "\"" + key + "\""
	i := strings.Index(raw, needle)
	if i == -1 {
		return ""
	}
	rest := raw[i+len(needle):]
	colon := strings.Index(rest, ":")
	if colon == -1 {
		return ""
	}
	rest = strings.TrimLeft(rest[colon+1:], " \t\r\n")
	if !strings.HasPrefix(rest, "\"") {
		return ""
	}
	rest = rest[1:]
	var b strings.Builder
	escaped := false
	for _, r := range rest {
		if escaped {
			switch r {
			case 'n':
				b.WriteRune('\n')
			case 'r':
				b.WriteRune('\r')
			case 't':
				b.WriteRune('\t')
			default:
				b.WriteRune(r)
			}
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if r == '"' {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}
