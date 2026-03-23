package app

import "fmt"

func (a *App) supportLogf(format string, args ...any) {
	if a == nil {
		return
	}
	a.logs.append("[support] " + fmt.Sprintf(format, args...))
}
