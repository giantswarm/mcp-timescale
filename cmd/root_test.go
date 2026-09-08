package cmd

import (
	"strings"
	"testing"
)

func TestRootCmdProperties(t *testing.T) {
	if rootCmd.Use != "mcp-timescale" {
		t.Errorf("Use = %q, want mcp-timescale", rootCmd.Use)
	}
	if !strings.Contains(rootCmd.Short, "Read-only") {
		t.Errorf("Short should say the server is read-only, got %q", rootCmd.Short)
	}
	for _, want := range []string{"Model Context Protocol", "TimescaleDB", "READ ONLY", "no write mode"} {
		if !strings.Contains(rootCmd.Long, want) {
			t.Errorf("Long should mention %q, got:\n%s", want, rootCmd.Long)
		}
	}
	if !rootCmd.SilenceUsage {
		t.Error("SilenceUsage should be set so a handled error does not print the usage text")
	}
}

func TestSetVersion(t *testing.T) {
	original := rootCmd.Version
	t.Cleanup(func() { rootCmd.Version = original })

	SetVersion("v1.2.3-test")
	if rootCmd.Version != "v1.2.3-test" {
		t.Errorf("rootCmd.Version = %q, want v1.2.3-test", rootCmd.Version)
	}
}

func TestRootCommandHasSubcommands(t *testing.T) {
	found := map[string]bool{}
	for _, c := range rootCmd.Commands() {
		found[c.Use] = true
	}
	for _, want := range []string{"serve", "version", "self-update"} {
		if !found[want] {
			t.Errorf("subcommand %q is missing; have %v", want, found)
		}
	}
}
