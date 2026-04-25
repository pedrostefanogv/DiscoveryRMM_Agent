//go:build !windows

package app

func detectPreferredLocale() string {
	if locale := detectLocaleFromEnv(); locale != "" {
		return locale
	}
	return defaultAppLocale
}
