//go:build !windows

package main

import "context"

func tryRunWindowsService(logFile string) (bool, error) {
	return false, nil
}

func newConsoleServiceContext() (context.Context, context.CancelFunc, error) {
	ctx, cancel := context.WithCancel(context.Background())
	return ctx, cancel, nil
}
