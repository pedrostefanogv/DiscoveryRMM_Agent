package supportchat

import "time"

// nowUnixNano existe para manter o fallback de newID testável.
func nowUnixNano() int64 { return time.Now().UnixNano() }
