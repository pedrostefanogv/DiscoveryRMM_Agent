//go:build darwin

package main

// systray does not expose title/tooltip setters on darwin in this dependency version.
func setTrayIcon(icon []byte) {}

func setTrayTitle(title string) {}

func setTrayTooltip(tooltip string) {}
