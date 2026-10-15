package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"fuckyourcli/internal/releasenotes"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: release-notes check-version|draft|validate|assemble|extract <version> [files...]")
	}
	command, version := args[0], args[1]
	if !releasenotes.ValidVersion(version) {
		return fmt.Errorf("version must be SemVer without a leading v")
	}
	read := func(index int) (string, error) {
		data, err := os.ReadFile(args[index])
		return string(data), err
	}
	switch {
	case command == "check-version" && len(args) == 2:
		return nil
	case command == "draft" && len(args) == 3:
		log, err := read(2)
		if err != nil {
			return err
		}
		commits, err := releasenotes.ParseLog(log)
		if err != nil {
			return err
		}
		section, err := releasenotes.Draft(version, time.Now(), commits)
		if err != nil {
			return err
		}
		_, err = io.WriteString(out, section)
		return err
	case command == "validate" && len(args) == 3:
		section, err := read(2)
		if err != nil {
			return err
		}
		return releasenotes.Validate(version, section)
	case command == "assemble" && len(args) == 4:
		section, err := read(2)
		if err != nil {
			return err
		}
		changelog, err := read(3)
		if err != nil {
			return err
		}
		assembled, err := releasenotes.Assemble(version, section, changelog)
		if err != nil {
			return err
		}
		_, err = io.WriteString(out, assembled)
		return err
	case command == "extract" && len(args) == 3:
		changelog, err := read(2)
		if err != nil {
			return err
		}
		section, err := releasenotes.Extract(version, changelog)
		if err != nil {
			return err
		}
		if err := releasenotes.Validate(version, section); err != nil {
			return err
		}
		_, err = io.WriteString(out, section)
		return err
	default:
		return fmt.Errorf("invalid command or arguments: %s", command)
	}
}
