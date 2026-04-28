package app

import (
	"strings"

	"discovery/internal/platform"

	"golang.org/x/text/language"
)

const defaultAppLocale = "pt-BR"

func normalizeSupportedLocale(raw string) string {
	value := strings.TrimSpace(strings.ReplaceAll(raw, "_", "-"))
	if value == "" {
		return defaultAppLocale
	}

	tag, err := language.Parse(value)
	if err != nil {
		lower := strings.ToLower(value)
		switch {
		case strings.HasPrefix(lower, "en"):
			return "en-US"
		case strings.HasPrefix(lower, "pt"):
			return defaultAppLocale
		default:
			return defaultAppLocale
		}
	}

	base, _ := tag.Base()
	switch base.String() {
	case "en":
		return "en-US"
	case "pt":
		return defaultAppLocale
	default:
		return defaultAppLocale
	}
}

func detectLocaleFromEnv() string {
	locale := platform.Locale()
	if locale == "" {
		return ""
	}
	return normalizeSupportedLocale(locale)
}
