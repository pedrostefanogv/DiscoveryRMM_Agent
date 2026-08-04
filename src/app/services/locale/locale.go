package locale

import (
	"strings"

	"discovery/app/core/platform"

	"golang.org/x/text/language"
)

const DefaultAppLocale = "pt-BR"

// NormalizeSupportedLocale normaliza um locale bruto para um formato suportado.
func NormalizeSupportedLocale(raw string) string {
	value := strings.TrimSpace(strings.ReplaceAll(raw, "_", "-"))
	if value == "" {
		return DefaultAppLocale
	}

	tag, err := language.Parse(value)
	if err != nil {
		lower := strings.ToLower(value)
		switch {
		case strings.HasPrefix(lower, "en"):
			return "en-US"
		case strings.HasPrefix(lower, "pt"):
			return DefaultAppLocale
		default:
			return DefaultAppLocale
		}
	}

	base, _ := tag.Base()
	switch base.String() {
	case "en":
		return "en-US"
	case "pt":
		return DefaultAppLocale
	default:
		return DefaultAppLocale
	}
}

// DetectLocaleFromEnv detecta o locale a partir do ambiente.
func DetectLocaleFromEnv() string {
	locale := platform.Locale()
	if locale == "" {
		return ""
	}
	return NormalizeSupportedLocale(locale)
}
