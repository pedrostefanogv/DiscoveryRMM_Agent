//go:build !windows

package windowsupdate

// gatherPreflightInput e um no-op em plataformas sem WUA: o preflight so existe
// para o Windows Update.
func gatherPreflightInput(int64, bool) PreflightInput {
	return PreflightInput{ServiceState: "unknown"}
}
