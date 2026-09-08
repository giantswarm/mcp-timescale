package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionCmd(t *testing.T) {
	tests := []struct {
		name      string
		version   string
		firstLine string
	}{
		{name: "dev version", version: "dev", firstLine: "mcp-timescale version dev"},
		{name: "semantic version", version: "v1.2.3", firstLine: "mcp-timescale version v1.2.3"},
		{name: "empty version", version: "", firstLine: "mcp-timescale version "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := rootCmd.Version
			t.Cleanup(func() { rootCmd.Version = original })
			rootCmd.Version = tt.version

			cmd := newVersionCmd()
			var buf bytes.Buffer
			cmd.SetOut(&buf)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}

			lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
			if len(lines) != 3 {
				t.Fatalf("want three lines (version, commit, built), got %q", buf.String())
			}
			if lines[0] != tt.firstLine {
				t.Errorf("first line = %q, want %q", lines[0], tt.firstLine)
			}
			if !strings.HasPrefix(lines[1], "commit: ") || !strings.HasPrefix(lines[2], "built:  ") {
				t.Errorf("commit and build time lines are malformed: %q", lines[1:])
			}
		})
	}
}

func TestVersionCmdProperties(t *testing.T) {
	cmd := newVersionCmd()
	if cmd.Use != "version" {
		t.Errorf("Use = %q", cmd.Use)
	}
	if cmd.Short != "Print the version number of mcp-timescale" {
		t.Errorf("Short = %q", cmd.Short)
	}
	if !strings.Contains(cmd.Long, "mcp-timescale") {
		t.Errorf("Long should name the binary, got %q", cmd.Long)
	}
}
