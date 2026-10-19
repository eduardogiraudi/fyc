package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Output caps for the tools that can read an unbounded amount of content
// (whole files, match sets). They protect the model context: a single read or
// grep answer must never swamp a turn. Every cut is announced by a note placed
// BEFORE the payload — a truncated answer must never look like a complete one,
// otherwise the model concludes that the rest of the file/matches do not exist.
const (
	// readMaxLines and readMaxBytes bound a single read/get_anchors window.
	readMaxLines = 2000
	readMaxBytes = 100 * 1024
	// grepMaxMatches is the default match cap; grepMaxMatchesHard is the ceiling
	// even when the caller asks for more via head_limit.
	grepMaxMatches     = 200
	grepMaxMatchesHard = 1000
	grepMaxBytes       = 64 * 1024
)

// leadingNote renders a truncation notice that goes before the payload.
func leadingNote(format string, args ...any) string {
	return "[" + fmt.Sprintf(format, args...) + "]\n"
}

// sizeLabel renders a byte count compactly for the notes.
func sizeLabel(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%d KB", n/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

// countLines counts the lines of text the way an editor does: a trailing
// newline terminates the last line instead of opening a new (empty) one.
func countLines(text string) int {
	if text == "" {
		return 0
	}
	n := strings.Count(text, "\n")
	if !strings.HasSuffix(text, "\n") {
		n++
	}
	return n
}

// truncateBytes cuts s to at most n bytes on a rune boundary.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// windowLines returns the lines [start, total) of text, limited to maxLines and
// maxBytes. midLine reports that the byte budget was exhausted inside a single
// line, i.e. the returned body ends mid-line.
func windowLines(text string, start, total, maxLines, maxBytes int) (body string, taken int, byteCut, midLine bool) {
	lines := strings.Split(text, "\n")
	end := start + maxLines
	if end > total {
		end = total
	}
	size := 0
	for i := start; i < end; i++ {
		if i > start && size+len(lines[i])+1 > maxBytes {
			end = i
			byteCut = true
			break
		}
		size += len(lines[i]) + 1
	}
	body = strings.Join(lines[start:end], "\n")
	taken = end - start
	if taken == 0 {
		taken = 1
		body = lines[start]
	}
	if len(body) > maxBytes {
		body = truncateBytes(body, maxBytes)
		byteCut, midLine = true, true
	}
	return body, taken, byteCut, midLine
}

// windowToolOutput applies the read caps to a line-oriented payload and
// prefixes the note describing what was left out. offset is 1-based; limit is
// the caller's line request (0 = as many as the caps allow). extra is appended
// to the note (empty for read).
func windowToolOutput(text string, offset, limit int, tool, extra string) string {
	total := countLines(text)
	if total == 0 {
		return text
	}
	if offset < 1 {
		offset = 1
	}
	if offset > total {
		return fmt.Sprintf("error: offset %d is beyond the end of the file (%d lines)", offset, total)
	}
	maxLines := readMaxLines
	if limit > 0 && limit < maxLines {
		maxLines = limit
	}
	start := offset - 1
	if start+maxLines >= total && len(text) <= readMaxBytes {
		// Nothing to cut: return the exact bytes.
		if start == 0 {
			return text
		}
		return leadingNote("%s: lines %d-%d of %d — end of file", tool, offset, total, total) +
			strings.Join(strings.Split(text, "\n")[start:total], "\n")
	}

	body, taken, byteCut, midLine := windowLines(text, start, total, maxLines, readMaxBytes)
	last := start + taken
	note := fmt.Sprintf("%s: lines %d-%d of %d (%s of %s)", tool, offset, last, total, sizeLabel(len(body)), sizeLabel(len(text)))
	if byteCut {
		note += ", byte budget reached"
	}
	switch {
	case midLine:
		// The body stops inside a line, so no offset can resume it.
		note += fmt.Sprintf(" — line %d is longer than the byte budget (inspect it with sh)", last)
	case last >= total:
		note += " — end of file"
	default:
		note += fmt.Sprintf(" — call %s with offset=%d to continue", tool, last+1)
	}
	if extra != "" {
		note += extra
	}
	return leadingNote("%s", note) + body
}

// optionalNumberArg reads a numeric tool argument, defaulting to 0 when absent
// (numberArg in lsp.go treats a missing key as an error, which fits its callers
// but not the optional paging parameters here).
func optionalNumberArg(args map[string]any, key string) int {
	if _, ok := args[key]; !ok {
		return 0
	}
	n, err := numberArg(args, key)
	if err != nil {
		return 0
	}
	return n
}

// grepHeadLimit returns the effective match cap for a grep call.
func grepHeadLimit(args map[string]any) int {
	n := optionalNumberArg(args, "head_limit")
	switch {
	case n <= 0:
		return grepMaxMatches
	case n > grepMaxMatchesHard:
		return grepMaxMatchesHard
	default:
		return n
	}
}
