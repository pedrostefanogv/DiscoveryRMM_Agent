//go:build !windows

package main

func detectStartupDebugMode() bool {
	return false
}
