package terminal

import "io"

// ShellKind define o tipo de shell.
type ShellKind string

const (
	ShellCmd       ShellKind = "cmd"
	ShellPowerShell ShellKind = "powershell"
	ShellBash      ShellKind = "bash"
)

// Ensure io import
var _ io.Reader
