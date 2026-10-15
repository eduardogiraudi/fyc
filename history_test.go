package main

import (
	"os"
	"strings"
	"testing"
)

// withHome points the user home (and thus the history file) at a temp dir.
func withHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return historyFilePath()
}

func TestHistoryDedupAndCap(t *testing.T) {
	path := withHome(t)
	s := &ChatSession{}

	// Consecutive duplicates collapse; a repeat after a different entry is kept.
	for _, e := range []string{"a", "a", "b", "a", "a"} {
		s.saveHistoryEntry(e)
	}
	got := readHistoryFile(path)
	want := []string{"a", "b", "a"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("history = %v, want %v", got, want)
	}

	// Fill beyond the cap: only the most recent maxHistoryEntries survive.
	for i := 0; i < maxHistoryEntries+20; i++ {
		s.saveHistoryEntry(strings.Repeat("x", i%7+1) + string(rune('A'+i%26)) + itoa(i))
	}
	got = readHistoryFile(path)
	if len(got) != maxHistoryEntries {
		t.Fatalf("history has %d entries, want cap %d", len(got), maxHistoryEntries)
	}
	if last := got[len(got)-1]; last != strings.Repeat("x", (maxHistoryEntries+19)%7+1)+string(rune('A'+(maxHistoryEntries+19)%26))+itoa(maxHistoryEntries+19) {
		t.Fatalf("newest entry = %q", last)
	}
}

func TestLoadHistoryEntriesTruncates(t *testing.T) {
	path := withHome(t)
	var lines []string
	for i := 0; i < maxHistoryEntries+100; i++ {
		lines = append(lines, itoa(i))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := loadHistoryEntries()
	if len(got) != maxHistoryEntries {
		t.Fatalf("loaded %d entries, want %d", len(got), maxHistoryEntries)
	}
	if got[len(got)-1] != itoa(maxHistoryEntries+99) {
		t.Fatalf("newest loaded entry = %q", got[len(got)-1])
	}
	if got[0] != itoa(100) {
		t.Fatalf("oldest loaded entry = %q, want %q", got[0], itoa(100))
	}
}

func TestSaveHistoryEntrySkipsEmptyAndTrailingNewline(t *testing.T) {
	path := withHome(t)
	s := &ChatSession{}
	s.saveHistoryEntry("")
	s.saveHistoryEntry("\n")
	s.saveHistoryEntry("real\n")
	got := readHistoryFile(path)
	if len(got) != 1 || got[0] != "real" {
		t.Fatalf("history = %v, want [real]", got)
	}
}

// itoa is a tiny helper to avoid importing strconv just for the tests.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
