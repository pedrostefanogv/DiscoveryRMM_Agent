//go:build !windows

package main

// suppressGameBarOverlay é no-op em plataformas não-Windows.
func suppressGameBarOverlay() {}
