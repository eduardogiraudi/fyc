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

func TestHistoryPreservesLargeMultilinePrompts(t *testing.T) {
	path := withHome(t)
	s := &ChatSession{}
	large := strings.Repeat("log: \"errore\" \\ percorso 日本語\r\n\n", 4000) + "fine"
	want := []string{"prima", large, "dopo\n\n  indentato\nultima"}
	for _, entry := range want {
		s.saveHistoryEntry(entry)
		s.saveHistoryEntry(entry)
	}
	got := loadHistoryEntries()
	if len(got) != len(want) {
		t.Fatalf("loaded %d prompts, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("prompt %d changed after persistence", i)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("history permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestHistoryMigratesLegacyEntries(t *testing.T) {
	path := withHome(t)
	legacy := "prima\n\n[\"literal JSON\"]\nprima\n"
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &ChatSession{}
	s.saveHistoryEntry("nuovo\nmultilinea")
	want := []string{"prima", "[\"literal JSON\"]", "prima", "nuovo\nmultilinea"}
	got := loadHistoryEntries()
	if len(got) != len(want) {
		t.Fatalf("history = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), historyFileHeader) {
		t.Fatal("legacy history was not migrated")
	}
}

func TestHistoryCapCountsPromptsNotLines(t *testing.T) {
	withHome(t)
	s := &ChatSession{}
	for i := 0; i < maxHistoryEntries+2; i++ {
		s.saveHistoryEntry(itoa(i) + "\nsecond line\n\nlast line")
	}
	got := loadHistoryEntries()
	if len(got) != maxHistoryEntries {
		t.Fatalf("loaded %d prompts, want %d", len(got), maxHistoryEntries)
	}
	if got[0] != "2\nsecond line\n\nlast line" || got[len(got)-1] != itoa(maxHistoryEntries+1)+"\nsecond line\n\nlast line" {
		t.Fatal("cap did not retain the newest whole prompts")
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
