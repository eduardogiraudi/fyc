package main

import (
	"testing"

	"fuckyourcli/types"
)

func TestMCPCommandValidation(t *testing.T) {
	for _, args := range [][]string{
		{"unknown"}, {"login"}, {"logout"}, {"status", "name", "extra"},
		{"login", "name", "--client-secret", "not-allowed"},
		{"login", "name", "--unknown"}, {"login", "name", "extra"},
		{"login", "name", "--callback-port", "-1"}, {"login", "name", "--callback-port", "65536"},
	} {
		if err := runMCPCommand(args); err == nil {
			t.Fatalf("accepted invalid MCP arguments: %v", args)
		}
	}
	for _, args := range [][]string{nil, {"--help"}, {"login", "--help"}, {"login", "name", "--help"}} {
		if err := runMCPCommand(args); err != nil {
			t.Fatalf("help %v: %v", args, err)
		}
	}
	if !hasMCPAuthorization(types.MCPServerConfig{Headers: map[string]string{"aUthOriZation": "Bearer secret"}}) {
		t.Fatal("manual Authorization precedence is case insensitive")
	}
}
