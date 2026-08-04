//go:build !windows

package locale

// DetectPreferredLocale detecta o locale preferido do usuário.
func DetectPreferredLocale() string {
	if locale := DetectLocaleFromEnv(); locale != "" {
		return locale
	}
	return DefaultAppLocale
}
