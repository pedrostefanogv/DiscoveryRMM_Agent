//go:build webrtc
// +build webrtc

package remotesession

import "time"

// Ensure time import used in webrtc.go
var _ = time.Now
