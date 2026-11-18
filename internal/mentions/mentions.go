package mentions

import (
	"strings"
	"unicode"
)

const MaxPathRunes = 1024

type Ref struct {
	Start, End int // Rune offsets, so the editor can replace only the mention.
	Path       string
}

// Scan excludes email/URL tokens, escaped mentions and matching code fences.
func Scan(text string, incomplete bool) []Ref {
	r := []rune(text)
	var refs []Ref
	fence := 0
	for i := 0; i < len(r); {
		if r[i] == '\\' && i+1 < len(r) {
			i += 2
			continue
		}
		if r[i] == '`' || r[i] == '~' && (i == 0 || r[i-1] == '\n') {
			ch := r[i]
			j := i
			for j < len(r) && r[j] == ch {
				j++
			}
			n := j - i
			if ch == '`' || n >= 3 {
				key := n
				if ch == '~' {
					key = -n
				}
				if fence == 0 {
					fence = key
				} else if fence == key {
					fence = 0
				}
			}
			i = j
			continue
		}
		if fence != 0 || r[i] != '@' || i > 0 && !unicode.IsSpace(r[i-1]) {
			i++
			continue
		}
		start := i
		i++
		quoted := i < len(r) && r[i] == '"'
		if quoted {
			i++
		}
		begin := i
		for i < len(r) && (quoted && r[i] != '"' && r[i] != '\n' || !quoted && !unicode.IsSpace(r[i]) && r[i] != '`') {
			i++
		}
		path := string(r[begin:i])
		closed := !quoted || i < len(r) && r[i] == '"'
		if quoted && closed {
			i++
		}
		if (closed || incomplete && i == len(r)) && (path != "" || incomplete) {
			refs = append(refs, Ref{start, i, path})
		}
	}
	return refs
}

func Active(text string) (Ref, bool) {
	refs := Scan(text, true)
	if len(refs) == 0 {
		return Ref{}, false
	}
	ref := refs[len(refs)-1]
	return ref, ref.End == len([]rune(text))
}

func Tag(path string) string {
	if strings.ContainsAny(path, " \t\r\n") {
		return "@\"" + path + "\""
	}
	return "@" + path
}
