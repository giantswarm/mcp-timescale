package cmd

import _ "embed"

// timescaleLogo is the Timescale logo the server advertises as its MCP icon.
// Source: simple-icons 16.36.0 icons/timescale.svg
// (https://cdn.jsdelivr.net/npm/simple-icons@16.36.0/icons/timescale.svg),
// CC0 1.0, drawn white on the brand colour #FDB515 the way muster draws its
// built-in brand marks.
//
//go:embed timescale.svg
var timescaleLogo []byte
