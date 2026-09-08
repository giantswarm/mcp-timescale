package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/giantswarm/mcp-timescale/pkg/project"
)

// newVersionCmd creates the command that prints the build identifiers. The
// version itself is rootCmd.Version (main sets it from pkg/project); commit
// and build time come from pkg/project directly.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version number of mcp-timescale",
		Long:  `All software has versions. This is mcp-timescale's.`,
		Run: func(cmd *cobra.Command, _ []string) {
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(out, "%s version %s\n", serviceName, rootCmd.Version)
			_, _ = fmt.Fprintf(out, "commit: %s\nbuilt:  %s\n", project.GitSHA(), project.BuildTimestamp())
		},
	}
}
