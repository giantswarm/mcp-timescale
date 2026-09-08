package main

import (
	"github.com/giantswarm/mcp-timescale/cmd"
	"github.com/giantswarm/mcp-timescale/pkg/project"
)

func main() {
	// The version comes from pkg/project: the architect CI and the devctl
	// Makefile stamp it through -ldflags -X at link time; a plain `go build`
	// falls back to Go's VCS build info.
	cmd.SetVersion(project.Version())

	cmd.Execute()
}
