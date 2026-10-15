package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"fuckyourcli/codexversion"
	"fuckyourcli/store"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, (codexversion.Fetcher{}).Latest))
}

func run(args []string, out, errOut io.Writer, fetch func(context.Context) (string, error)) int {
	flags := flag.NewFlagSet("update-codex-version", flag.ContinueOnError)
	flags.SetOutput(errOut)
	file := flags.String("file", "codexversion/VERSION", "tracked Codex fallback file")
	write := flags.Bool("write", false, "atomically update the tracked fallback before release")
	offline := flags.Bool("offline", false, "use the fallback without HTTP (reproducible builds)")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 1
	}
	data, err := os.ReadFile(*file)
	if err != nil {
		fmt.Fprintf(errOut, "error: read Codex fallback: %v\n", err)
		return 1
	}
	version := strings.TrimSpace(string(data))
	if !codexversion.Valid(version) {
		fmt.Fprintln(errOut, "error: Codex fallback file is not a valid stable version")
		return 1
	}
	if !*offline {
		latest, err := fetch(context.Background())
		if err != nil || !codexversion.Valid(latest) {
			fmt.Fprintf(errOut, "Warning: Codex version refresh failed (%v); keeping %s\n", err, version)
		} else {
			version = codexversion.Newer(version, latest)
		}
	}
	if *write && version != strings.TrimSpace(string(data)) {
		if err := store.WriteFile(*file, []byte(version+"\n"), 0o644); err != nil {
			fmt.Fprintf(errOut, "Warning: cannot update Codex fallback (%v); keeping %s\n", err, strings.TrimSpace(string(data)))
			version = strings.TrimSpace(string(data))
		}
	}
	fmt.Fprintln(out, version)
	return 0
}
