package app

import "fmt"

func (a *App) supportLogf(format string, args ...any) {
	if a == nil {
		return
	}
	a.Logs.Append("[support] " + fmt.Sprintf(format, args...))
}
