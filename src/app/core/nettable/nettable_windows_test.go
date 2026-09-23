//go:build windows

package nettable

import "testing"

// TestPortDecodesNetworkOrderPort trava o decode: as tabelas MIB guardam a
// porta em network byte order, então o DWORD lido em little-endian precisa ser
// desempilhado. Antes da correção, 135 (RPC) era reportado como 34560 e
// 3389 (RDP) como 15629.
func TestPortDecodesNetworkOrderPort(t *testing.T) {
	cases := map[uint32]int{
		0x8700: 135,   // RPC endpoint mapper
		0x3D0D: 3389,  // RDP
		0x78A0: 41080, // faixa HTTP P2P do agente (DefaultP2PPortRangeStart)
		0x5000: 80, // HTTP (0x0050 em network byte order)
	}
	for raw, want := range cases {
		if got := Port(raw); got != want {
			t.Errorf("Port(0x%X) = %d, want %d", raw, got, want)
		}
	}
}

// TestTCPStateName garante os nomes MIB_TCP_STATE usados pela coluna State.
func TestTCPStateName(t *testing.T) {
	cases := map[uint32]string{
		1: "CLOSED", 2: "LISTEN", 3: "SYN_SENT", 4: "SYN_RCVD",
		5: "ESTABLISHED", 8: "CLOSE_WAIT", 11: "TIME_WAIT",
	}
	for state, want := range cases {
		if got := TCPStateName(state); got != want {
			t.Errorf("TCPStateName(%d) = %q, want %q", state, got, want)
		}
	}
	if got := TCPStateName(999); got != "" {
		t.Errorf("TCPStateName(999) = %q, want vazio", got)
	}
}
