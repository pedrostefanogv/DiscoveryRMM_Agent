//go:build !windows

package app

// currentProcessSessionID é no-op fora do Windows (não há sessões WTS).
func currentProcessSessionID() uint32 { return 0 }
