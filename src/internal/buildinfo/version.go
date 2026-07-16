package buildinfo

// Version is set at build time via:
// -ldflags "-X discovery/internal/buildinfo.Version=x.y.z"
var Version = "0.0.0"

// Commit is set at build time via:
// -ldflags "-X discovery/internal/buildinfo.Commit=<git rev-parse --short HEAD>"
var Commit = "unknown"
