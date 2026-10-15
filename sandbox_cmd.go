package main

import (
	"fmt"
	"os"
	"strings"

	"fuckyourcli/tools"
)

// runSandboxCommand implements `fuckyourcli sandbox <setup|status|reset>`
// It is a plain CLI surface — it does not enter
// the chat — so the one-time elevated setup is reachable without the TUI.
func runSandboxCommand(args []string) error {
	if len(args) == 0 {
		printSandboxUsage()
		return nil
	}
	switch args[0] {
	case "setup":
		return runSandboxSetup(args[1:])
	case "status":
		return runSandboxStatus()
	case "reset":
		return runSandboxReset()
	case "help", "-h", "--help":
		printSandboxUsage()
		return nil
	default:
		printSandboxUsage()
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func printSandboxUsage() {
	fmt.Println("Usage:")
	fmt.Println("  fuckyourcli sandbox setup [--dry-run] [--force] [--workspace DIR]")
	fmt.Println("  fuckyourcli sandbox status")
	fmt.Println("  fuckyourcli sandbox reset")
	fmt.Println()
	fmt.Println("The dedicated-user sandbox (Windows): a one-time elevated setup that creates a")
	fmt.Println("local account and protects its credentials with DPAPI. It plants no ACE: reads")
	fmt.Println("come from the runtime allow-list and BUILTIN\\Users.")
	fmt.Println("`setup` raises UAC by itself; use --dry-run to preview without changing anything.")
	fmt.Println("On other platforms these commands are no-ops.")
}

func runSandboxSetup(args []string) error {
	opts := tools.SandboxSetupOptions{Log: func(line string) { fmt.Println("  | " + line) }}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--dry-run" || a == "-n":
			opts.DryRun = true
		case a == "--force" || a == "-f":
			opts.Force = true
		case a == "--workspace" && i+1 < len(args):
			i++
			opts.Workspace = args[i]
		case strings.HasPrefix(a, "--workspace="):
			opts.Workspace = strings.TrimPrefix(a, "--workspace=")
		default:
			return fmt.Errorf("unknown flag %q", a)
		}
	}
	if opts.Workspace == "" {
		if wd, err := os.Getwd(); err == nil {
			opts.Workspace = wd
		}
	}

	res, err := tools.SandboxSetup(opts)
	if err != nil {
		return err
	}
	if !res.Supported {
		fmt.Println("dedicated-user sandbox: not supported on this platform")
		for _, n := range res.Notes {
			fmt.Println("  " + n)
		}
		return nil
	}

	fmt.Printf("sandbox setup — platform: %s\n", res.Platform)
	fmt.Printf("  user:   %s\n", res.User)
	fmt.Printf("  group:  %s\n", res.Group)
	fmt.Printf("  version: %d\n", res.Version)
	switch {
	case res.AlreadyDone:
		fmt.Println("  state:  already set up (idempotent, nothing to do)")
	case res.ReLaunched:
		fmt.Println("  state:  handed to an elevated copy (UAC) — see the log below")
	case opts.DryRun:
		fmt.Println("  state:  dry-run (nothing was created)")
	default:
		fmt.Println("  state:  done")
	}

	fmt.Printf("  credentials: %s\n", res.SecretsPath)
	fmt.Printf("  log:         %s\n", res.LogPath)
	for _, n := range res.Notes {
		fmt.Println("  note: " + n)
	}
	return nil
}

func runSandboxStatus() error {
	wd, _ := os.Getwd()
	st := tools.SandboxSetupStatus(wd)
	if !st.Supported {
		fmt.Println("dedicated-user sandbox: not supported on this platform")
		return nil
	}
	fmt.Printf("sandbox status — platform: %s\n", st.Platform)
	fmt.Printf("  elevated now:   %v\n", st.Elevated)
	fmt.Printf("  user:           %s (exists: %v)\n", st.User, st.UserExists)
	fmt.Printf("  group:          %s (exists: %v)\n", st.Group, st.GroupExists)
	fmt.Printf("  member:         %v\n", st.MemberOfGroup)
	fmt.Printf("  setup version:  %d\n", st.SetupVersion)
	fmt.Printf("  credentials:    %v (%s)\n", st.SecretsPresent, st.SecretsPath)
	fmt.Printf("  ready:          %v\n", st.Ready)
	fmt.Printf("  log:            %s\n", st.LogPath)
	if st.Err != "" {
		fmt.Println("  error: " + st.Err)
	}
	if !st.Ready {
		fmt.Println("  hint: run `fuckyourcli sandbox setup` (raises UAC once)")
	}
	return nil
}

func runSandboxReset() error {
	res, err := tools.SandboxUsersReset()
	if err != nil {
		return err
	}
	if !res.Supported {
		fmt.Println("dedicated-user sandbox: not supported on this platform")
		return nil
	}
	fmt.Printf("sandbox reset — platform: %s\n", res.Platform)
	fmt.Printf("  user deleted:   %v\n", res.UserDeleted)
	fmt.Printf("  group deleted:  %v\n", res.GroupDeleted)
	fmt.Printf("  ACEs stripped:  %d\n", res.StrippedACLs)
	if len(res.RemovedFiles) > 0 {
		fmt.Printf("  files removed (%d):\n", len(res.RemovedFiles))
		for _, f := range res.RemovedFiles {
			fmt.Println("    - " + f)
		}
	}
	for _, n := range res.Notes {
		fmt.Println("  note: " + n)
	}
	if !res.UserDeleted && !res.GroupDeleted && res.StrippedACLs == 0 && len(res.RemovedFiles) == 0 {
		fmt.Println("  nothing to clean up")
	} else if !strings.Contains(res.Platform, "not") {
		fmt.Println("  note: if a step was skipped, re-run from an elevated shell")
	}
	return nil
}
