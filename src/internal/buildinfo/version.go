package buildinfo

// Version is set at build time via:
// -ldflags "-X discovery/internal/buildinfo.Version=x.y.z"
var Version = "0.0.0"
