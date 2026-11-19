package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"time"

	"fuckyourcli/config"
	"fuckyourcli/mcpauth"
	"fuckyourcli/types"
)

const mcpUsage = `Usage:
  fuckyourcli mcp status [name]
  fuckyourcli mcp login <name> [--no-browser] [--client-id ID] [--client-secret-env VAR] [--callback-port PORT] [--scopes "scope1 scope2"]
  fuckyourcli mcp logout <name>

Names include approved plugins (for example my-plugin--my-server).
OAuth credentials are stored only in auth.json; tokens renew automatically.
Login uses a local loopback callback and waits up to five minutes.
Logout removes local credentials; it does not revoke tokens at the issuer.
`

func runMCPCommand(args []string) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h")) {
		fmt.Print(mcpUsage)
		return nil
	}
	action := args[0]
	if action != "status" && action != "login" && action != "logout" {
		return fmt.Errorf("unknown subcommand %q; use fuckyourcli mcp --help", action)
	}
	if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
		fmt.Print(mcpUsage)
		return nil
	}
	name := ""
	if len(args) > 1 {
		name = args[1]
	}
	if (action != "status" && name == "") || strings.HasPrefix(name, "-") {
		return fmt.Errorf("%s requires a server name; use fuckyourcli mcp --help", action)
	}
	var noBrowser bool
	var clientID, secretEnv, scopes string
	var callbackPort int
	if action == "login" {
		flags := flag.NewFlagSet("mcp login", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		flags.BoolVar(&noBrowser, "no-browser", false, "print URL without opening a browser")
		flags.StringVar(&clientID, "client-id", "", "preregistered OAuth client ID")
		flags.StringVar(&secretEnv, "client-secret-env", "", "environment variable containing the client secret")
		flags.StringVar(&scopes, "scopes", "", "space-separated OAuth scopes")
		flags.IntVar(&callbackPort, "callback-port", 0, "fixed loopback callback port (default: dynamic)")
		if err := flags.Parse(args[2:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				fmt.Print(mcpUsage)
				return nil
			}
			return err
		}
		if callbackPort < 0 || callbackPort > 65535 {
			return fmt.Errorf("invalid OAuth callback port")
		}
		if flags.NArg() != 0 {
			return fmt.Errorf("unexpected arguments; use fuckyourcli mcp --help")
		}
	} else if len(args) > 2 {
		return fmt.Errorf("unexpected arguments; use fuckyourcli mcp --help")
	}
	workspace, err := os.Getwd()
	if err != nil {
		return err
	}
	servers := config.SessionMCPServers(workspace)
	secrets := config.MCPOAuthStore{}
	matched := false
	for _, cfg := range servers {
		if name != "" && cfg.Name != name {
			continue
		}
		matched = true
		if action == "status" {
			status := "OAuth not applicable (stdio)"
			if cfg.Transport == "sse" || cfg.Transport == "streamableHttp" {
				if hasMCPAuthorization(cfg) {
					status = "manual Authorization header (takes precedence over OAuth)"
				} else {
					status, err = mcpauth.Status(cfg, secrets)
					if err != nil {
						return err
					}
				}
			}
			if cfg.Disabled {
				status = "disabled; " + status
			}
			fmt.Printf("%s: %s\n", cfg.Name, status)
			continue
		}
		if action == "logout" {
			if err := mcpauth.Logout(cfg, secrets); err != nil {
				return err
			}
			fmt.Printf("Removed local OAuth credentials for %s.\n", cfg.Name)
			return nil
		}
		if hasMCPAuthorization(cfg) {
			return fmt.Errorf("server %q has a manual Authorization header; remove it before using OAuth", cfg.Name)
		}
		clientSecret := ""
		if secretEnv != "" {
			if clientID == "" {
				return fmt.Errorf("--client-secret-env requires --client-id")
			}
			clientSecret = os.Getenv(secretEnv)
			if clientSecret == "" {
				return fmt.Errorf("client-secret environment variable is unset or empty")
			}
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		err := mcpauth.Login(ctx, cfg, secrets, mcpauth.LoginOptions{
			ClientID: clientID, ClientSecret: clientSecret, Scopes: strings.Fields(scopes), CallbackPort: callbackPort,
			OpenURL: func(authURL string) error {
				fmt.Printf("Authorize %s in your browser:\n%s\n", cfg.Name, authURL)
				if !noBrowser {
					if err := openMCPBrowser(authURL); err != nil {
						fmt.Fprintln(os.Stderr, "Could not open a browser. Open the URL above manually.")
					}
				}
				fmt.Println("Waiting for the local OAuth callback (Ctrl+C to cancel)...")
				return nil
			},
		})
		if err != nil {
			return err
		}
		fmt.Printf("Authenticated %s. Start a new chat to load its MCP tools.\n", cfg.Name)
		return nil
	}
	if !matched && name != "" {
		return fmt.Errorf("MCP server %q not found in this workspace; run fuckyourcli mcp status", name)
	}
	if !matched {
		fmt.Println("No MCP servers configured in this workspace.")
	}
	return nil
}

func hasMCPAuthorization(cfg types.MCPServerConfig) bool {
	for name := range cfg.Headers {
		if strings.EqualFold(name, "Authorization") {
			return true
		}
	}
	return false
}

func openMCPBrowser(authURL string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", authURL)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", authURL)
	default:
		command = exec.Command("xdg-open", authURL)
	}
	if err := command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}
