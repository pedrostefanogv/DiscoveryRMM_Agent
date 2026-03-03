//go:build !darwin

package main

import "github.com/energye/systray"

func setTrayIcon(icon []byte) {
	systray.SetIcon(icon)
}

func setTrayTitle(title string) {
	systray.SetTitle(title)
}

func setTrayTooltip(tooltip string) {
	systray.SetTooltip(tooltip)
}
