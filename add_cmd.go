package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"fuckyourcli/config"
	"fuckyourcli/plugins"
	"fuckyourcli/ui"
)

const addUsage = `Usage: fuckyourcli add <source> [--project|--global] [--ref REF] [--subdir PATH] [--replace] [--yes]

Sources: local plugin directory, owner/repo, GitHub repository URL, HTTPS ZIP URL.
Default scope: current project. GitHub defaults to its default branch (HEAD).
Only Agent Plugins 1.0.0 plugin.json + skills/ + mcp.json are supported.
Local sources skip .git and dependency/cache directories (.venv, node_modules, __pycache__, ...).
`

type addOptions struct {
	source, ref, subdir        string
	global, replace, yes, help bool
}

func parseAddOptions(args []string, out io.Writer) (addOptions, error) {
	var o addOptions
	flags := flag.NewFlagSet("add", flag.ContinueOnError)
	flags.SetOutput(out)
	var project bool
	flags.BoolVar(&o.global, "global", false, "install for every project")
	flags.BoolVar(&project, "project", false, "install for the current project (default)")
	flags.BoolVar(&o.replace, "replace", false, "replace an existing plugin after review")
	flags.BoolVar(&o.yes, "yes", false, "explicitly approve installation without a prompt")
	flags.StringVar(&o.ref, "ref", "", "GitHub branch, tag, or commit")
	flags.StringVar(&o.subdir, "subdir", "", "plugin directory within the source")
	flags.BoolVar(&o.help, "help", false, "show help")
	flags.BoolVar(&o.help, "h", false, "show help")
	// Accept the source both before and after flags, without losing flag values.
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			if o.source != "" {
				return o, fmt.Errorf("expected exactly one source")
			}
			o.source = a
			continue
		}
		rest = append(rest, a)
		if a == "--ref" || a == "-ref" || a == "--subdir" || a == "-subdir" {
			if i+1 >= len(args) {
				return o, fmt.Errorf("%s requires a value", a)
			}
			i++
			rest = append(rest, args[i])
		}
	}
	flags.Usage = func() { fmt.Fprint(out, addUsage) }
	if err := flags.Parse(rest); err != nil {
		return o, err
	}
	if o.help {
		return o, nil
	}
	if o.source == "" {
		return o, fmt.Errorf("missing plugin source\n%s", addUsage)
	}
	if project && o.global {
		return o, fmt.Errorf("choose either --project or --global")
	}
	return o, nil
}

func runAddCommand(args []string) error {
	o, err := parseAddOptions(args, os.Stdout)
	if err != nil {
		return err
	}
	if o.help {
		fmt.Print(addUsage)
		return nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	return installAdd(context.Background(), o, cwd, os.Stdout, func() bool {
		return ui.SelectOption("Trust this plugin? MCP commands run on the host, outside the tool sandbox.", []string{"Cancel", "Install"}) == 1
	})
}

func installAdd(ctx context.Context, o addOptions, cwd string, out io.Writer, approve func() bool) error {
	fetchCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	p, err := plugins.Prepare(fetchCtx, o.source, o.ref, o.subdir)
	cancel()
	if err != nil {
		return err
	}
	dest, err := plugins.Destination(cwd, o.global, p.Name)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Plugin: %q  version: %q\nDescription: %q\nDestination: %q\n", p.Name, p.Version, p.Description, dest)
	for _, s := range p.Skills {
		fmt.Fprintf(out, "  skill %s: %q\n", s.Name, s.Description)
	}
	// Preview the manifest, never the resolved credentials.
	for _, name := range sortedPluginKeys(p.Servers) {
		s := p.Servers[name]
		fmt.Fprintf(out, "  MCP %s--%s (%s)", p.Name, name, s.Type)
		if s.Type == "stdio" {
			fmt.Fprintf(out, " command=%q args=%q cwd=%q", s.Command, s.Args, s.Cwd)
		} else {
			fmt.Fprintf(out, " url=%q", s.URL)
		}
		fmt.Fprintln(out)
		for _, key := range sortedPluginKeys(s.Env) {
			fmt.Fprintf(out, "    env: %q (value hidden)\n", key)
		}
		for _, key := range sortedPluginKeys(s.Headers) {
			fmt.Fprintf(out, "    header: %q (value hidden)\n", key)
		}
	}
	missing := config.MissingPluginVariables(pluginValues(p))
	for _, name := range missing {
		fmt.Fprintf(out, "  Required variable: %s (environment or auth.json)\n", name)
	}
	fmt.Fprintln(out, "Scripts and hooks are not executed. MCP servers start in the next session; local commands run outside the tool sandbox.")
	if !o.yes && !approve() {
		fmt.Fprintln(out, "Installation cancelled.")
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := plugins.Install(p, cwd, o.global, o.replace); err != nil {
		return err
	}
	fmt.Fprintf(out, "Installed %s. Start a new session; matching skills are read on demand.\n", p.Name)
	for _, name := range missing {
		if o.yes {
			fmt.Fprintf(out, "Set %s before starting a session; its MCP plugin stays disabled until configured.\n", name)
			continue
		}
		value := ui.InputPassword("Set " + name + " (saved in auth.json; leave blank to configure later)")
		if value == "" {
			break
		}
		if err := config.SavePluginSecret(name, value); err != nil {
			return err
		}
	}
	return nil
}

func sortedPluginKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func pluginValues(p *plugins.Package) []string {
	var values []string
	for _, name := range sortedPluginKeys(p.Servers) {
		s := p.Servers[name]
		values = append(values, s.Command, s.Cwd, s.URL)
		values = append(values, s.Args...)
		for _, k := range sortedPluginKeys(s.Env) {
			values = append(values, s.Env[k])
		}
		for _, k := range sortedPluginKeys(s.Headers) {
			values = append(values, s.Headers[k])
		}
	}
	return values
}
