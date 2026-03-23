package app

import (
	"encoding/json"
	"fmt"
	"strings"
)

func toInt(values ...any) int {
	for _, v := range values {
		switch n := v.(type) {
		case float64:
			return int(n)
		case float32:
			return int(n)
		case int:
			return n
		case int64:
			return int(n)
		case json.Number:
			if i, err := n.Int64(); err == nil {
				return int(i)
			}
		case string:
			s := strings.TrimSpace(n)
			if s == "" {
				continue
			}
			var parsed int
			if _, err := fmt.Sscanf(s, "%d", &parsed); err == nil {
				return parsed
			}
		}
	}
	return 0
}

func toBool(values ...any) bool {
	for _, v := range values {
		switch b := v.(type) {
		case bool:
			return b
		case string:
			s := strings.ToLower(strings.TrimSpace(b))
			if s == "true" || s == "1" || s == "yes" || s == "sim" {
				return true
			}
			if s == "false" || s == "0" || s == "no" || s == "nao" || s == "não" {
				return false
			}
		case float64:
			return b != 0
		case int:
			return b != 0
		}
	}
	return false
}
