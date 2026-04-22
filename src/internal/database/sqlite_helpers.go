package database

import (
	"database/sql"
	"strings"
	"time"
)

func nullIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullUnix(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.Unix()
}

func timeFromNullUnix(value sql.NullInt64) time.Time {
	if !value.Valid || value.Int64 <= 0 {
		return time.Time{}
	}
	return time.Unix(value.Int64, 0)
}
