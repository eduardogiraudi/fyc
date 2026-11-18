package main

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fuckyourcli/tools"
)

func TestPreparePromptFiles(t *testing.T) {
	root := t.TempDir()
	for name, data := range map[string]string{"test.py": "print('ciao')\n", "with spaces.py": "α\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	input := "inspect @test.py @test.py and @\"with spaces.py\""
	msg, names, err := preparePromptFiles(input, root, tools.LoadIgnoreFiles(root))
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || !strings.HasPrefix(msg.Content, input) || strings.Count(msg.Content, "print('ciao')") != 1 || !strings.Contains(msg.Content, "α\n") {
		t.Fatalf("unexpected prompt: %+v, %v", msg, names)
	}
	plain := "email@example.com `@test.py` \\@test.py"
	msg, names, err = preparePromptFiles(plain, root, nil)
	if err != nil || msg.Content != plain || len(names) != 0 {
		t.Fatalf("literal mentions changed: %+v, %v, %v", msg, names, err)
	}
}

func TestPreparePromptFilesImage(t *testing.T) {
	root := t.TempDir()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "foto.png"), b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	msg, names, err := preparePromptFiles("look @foto.png", root, tools.LoadIgnoreFiles(root))
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || len(msg.Images) != 1 || msg.Images[0].MIMEType != "image/png" || msg.Images[0].Width != 2 {
		t.Fatalf("unexpected attachment: %+v", msg)
	}
}

func TestPreparePromptFilesRejects(t *testing.T) {
	root := t.TempDir()
	for name, data := range map[string][]byte{
		"binary.bin": {0, 1, 2}, "huge.txt": []byte(strings.Repeat("x", tools.MaxPromptFileBytes+1)),
		"secret.txt": []byte("secret"), ".fycignore": []byte("secret.txt\n"),
		"budget.txt": []byte(strings.Repeat("x", maxPromptFileTokens*4+1)),
	} {
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"@missing.txt", "@../outside.txt", "@binary.bin", "@huge.txt", "@budget.txt", "@secret.txt", "@dir", "@" + strings.Repeat("x", 1025)} {
		msg, names, err := preparePromptFiles(input, root, tools.LoadIgnoreFiles(root))
		if err == nil || msg.Content != "" || len(names) != 0 {
			t.Errorf("expected atomic rejection for %q: %+v %v %v", input, msg, names, err)
		}
	}
}

func TestPreparePromptFilesCountLimit(t *testing.T) {
	root := t.TempDir()
	var input string
	for i := 0; i < maxPromptFiles+1; i++ {
		name := fmt.Sprintf("%d.txt", i)
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
		input += " @" + name
	}
	if _, _, err := preparePromptFiles(input, root, nil); err == nil {
		t.Fatal("accepted too many files")
	}
}

func TestExpandedInstructionsDoNotAttachFiles(t *testing.T) {
	s := &ChatSession{RestrictedRoot: t.TempDir(), Sink: &recordSink{}}
	msg, ok := s.attachPromptFiles("skill instructions mention @missing.py", "user question")
	if !ok || msg.Content != "skill instructions mention @missing.py" {
		t.Fatalf("expanded instructions attached files: %+v", msg)
	}
}

func TestAttachmentErrorDoesNotStartTurn(t *testing.T) {
	withHome(t)
	sink := &recordSink{}
	s := &ChatSession{RestrictedRoot: t.TempDir(), Sink: sink}
	if !s.ProcessInput("inspect @missing.py") {
		t.Fatal("attachment failure exited chat")
	}
	if len(s.ChatHistory) != 0 || !strings.Contains(sink.text(), "Attachment error:") {
		t.Fatalf("failed attachment changed history or hid error: %s", sink.text())
	}
}
