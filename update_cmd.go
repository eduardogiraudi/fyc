// update_cmd.go — `fuckyourcli status` and `fuckyourcli update`.
package main

import (
	"context"
	"fmt"
	"fuckyourcli/ui"
	"fuckyourcli/updater"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// runUpdate implements both `status` (report only) and `update` (report and
// install). The target version comes from FYC_UPDATE_VERSION and can be
// overridden by the first positional argument.
func runUpdate(s *ChatSession, checkOnly bool, args []string) error {
	version := strings.TrimSpace(os.Getenv("FYC_UPDATE_VERSION"))
	skipSandboxSetup := os.Getenv("FYC_SKIP_SANDBOX_SETUP") == "1"
	for _, a := range args {
		switch {
		case a == "-h" || a == "--help":
			printUpdateHelp(checkOnly)
			return nil
		case a == "--no-sandbox-setup" || a == "-NoSandboxSetup":
			skipSandboxSetup = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %q", a)
		default:
			version = strings.TrimSpace(a)
		}
	}

	opts := updater.Options{
		Repo:    strings.TrimSpace(os.Getenv("FYC_UPDATE_REPO")),
		Version: version,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	current := s.Version
	if current == "" {
		current = "dev"
	}

	if checkOnly {
		st, err := updater.Check(ctx, opts, current)
		if err != nil {
			return err
		}
		reportStatus(st)
		return nil
	}

	st, path, err := updater.Update(ctx, opts, current)
	if err != nil {
		return err
	}
	// Update returns an empty path when nothing was installed.
	if path == "" {
		fmt.Printf("%s already up to date (%s)\n", ui.OkStyle.Render("✓"), st.Current)
		return nil
	}

	fmt.Printf("%s %s → %s\n", ui.InfoStyle.Render("update"), st.Current, ui.OkStyle.Render(st.Latest))
	fmt.Printf("  asset:    %s\n", st.AssetName)
	if st.ChecksumVerified {
		fmt.Printf("  checksum: verified\n")
	}
	fmt.Printf("  installed: %s\n", path)
	// The Windows sandbox runs a copy of the binary under %ProgramData% whose
	// hash is recorded at setup time: a fresh binary invalidates it and the
	// sandboxed `sh` refuses to start until `sandbox setup` runs again.
	if runtime.GOOS == "windows" {
		if skipSandboxSetup {
			fmt.Printf("  sandbox: not configured (--no-sandbox-setup); run: fuckyourcli sandbox setup\n")
		} else {
			refreshSandboxSetup(path)
		}
	}
	fmt.Printf("Restart the CLI to use %s.\n", st.Latest)
	return nil
}

// reportStatus prints the outcome of a check without changing anything.
func reportStatus(st *updater.Status) {
	fmt.Printf("%s %s (%s/%s)\n", ui.KeyStyle.Render("fuckyourcli"), st.Current, runtime.GOOS, runtime.GOARCH)
	if st.Requested {
		fmt.Printf("  requested: %s\n", st.Latest)
	} else {
		fmt.Printf("  latest:    %s\n", st.Latest)
	}
	fmt.Printf("  asset:     %s\n", st.AssetName)
	switch {
	case st.Requested:
		fmt.Printf("  update:    run `fuckyourcli update %s` to install it\n", st.Latest)
	case st.UpToDate:
		fmt.Printf("  update:    %s\n", ui.OkStyle.Render("up to date"))
	default:
		fmt.Printf("  update:    %s — run `fuckyourcli update`\n", ui.InfoStyle.Render("available"))
	}
}

func printUpdateHelp(checkOnly bool) {
	if checkOnly {
		fmt.Println("Usage: fuckyourcli status [version]")
		fmt.Println()
		fmt.Println("Reports whether a newer release is published, without installing it.")
		fmt.Println("With a version it reports that release instead of the latest one.")
		fmt.Println("Environment: FYC_UPDATE_VERSION, FYC_UPDATE_REPO.")
		return
	}
	fmt.Println("Usage: fuckyourcli update [version] [--no-sandbox-setup]")
	fmt.Println()
	fmt.Println("Downloads the release asset for this platform, verifies its SHA-256")
	fmt.Println("against the release checksums.txt and replaces the running binary.")
	fmt.Println()
	fmt.Println("Without a version the latest release is used. A version may carry or omit")
	fmt.Println("the leading \"v\" (0.1.0 or v0.1.0) and is installed even when it is older")
	fmt.Println("than the running binary (pinning/downgrading).")
	fmt.Println()
	fmt.Println("On Windows the dedicated-user sandbox is reconfigured afterwards (one")
	fmt.Println("UAC prompt), because the runner copy's hash changes with the binary;")
	fmt.Println("skip it with --no-sandbox-setup or FYC_SKIP_SANDBOX_SETUP=1.")
	fmt.Println()
	fmt.Println("Environment: FYC_UPDATE_VERSION (same as the argument),")
	fmt.Println("FYC_UPDATE_REPO (owner/name of a public repository).")
}

// refreshSandboxSetup re-creates the Windows dedicated-user sandbox: the setup
// re-copies the runner under %ProgramData% and records its new hash. Non-fatal:
// a declined UAC leaves the CLI usable, only the sandboxed `sh` keeps refusing
// to start.
func refreshSandboxSetup(bin string) {
	fmt.Printf("  configuring the dedicated sandbox (one-time: it will ask for UAC)...\n")
	cmd := exec.Command(bin, "sandbox", "setup")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("  %s sandbox setup not completed (%v); run it later: fuckyourcli sandbox setup\n", ui.TipStyle.Render("note:"), err)
		return
	}
	fmt.Printf("  sandbox: ready\n")
}
