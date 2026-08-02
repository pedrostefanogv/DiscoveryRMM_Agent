package agentconn

import "testing"

func TestIsWSSLabel(t *testing.T) {
	cases := []struct {
		label string
		want  bool
	}{
		{"nats", false},
		{"nats-ws", true},
		{"nats-wss", true},
		{"nats-ws/wss", true},
		{"NATS-WSS", true},
		{"", false},
		{"nats (api-fallback)", false},
		{"nats-wss (api-fallback)", false},
	}
	for _, c := range cases {
		if got := isWSSLabel(c.label); got != c.want {
			t.Errorf("isWSSLabel(%q) = %v, esperado %v", c.label, got, c.want)
		}
	}
}
