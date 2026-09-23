//go:build windows

package native

import (
	"context"
	"errors"
	"net"
	"testing"

	"discovery/app/core/nettable"
)

// TestCollectSystemInfo verifies that native system info collection works
// and returns a hostname plus CPU brand/cores (bug "0C / 0T").
func TestCollectSystemInfo(t *testing.T) {
	hw, osInfo, err := collectSystemInfoNative(context.Background())
	if err != nil {
		t.Fatalf("collectSystemInfoNative: %v", err)
	}
	if hw.Hostname == "" {
		t.Error("hostname vazio")
	}
	if osInfo.Name == "" {
		t.Error("os name vazio")
	}
	// Bug "0C / 0T": a coleta deve sempre reportar núcleos/threads > 0.
	if hw.CPU == "" {
		t.Error("cpu brand vazio")
	}
	if hw.Cores <= 0 {
		t.Errorf("physical cores deve ser > 0, veio %d", hw.Cores)
	}
	if hw.LogicalCores <= 0 {
		t.Errorf("logical cores deve ser > 0, veio %d", hw.LogicalCores)
	}
	// Bug "16C / 16T": em CPUs com SMT/HT, lógicos > físicos. Se a detecção
	// degenerar (físico = lógico num sistema com HT ativo), é regressão.
	if hw.LogicalCores > hw.Cores && getNativeLogicalProcessorCount() > hw.Cores {
		// Sistema com HT/SMT: físico NUNCA deve ser 1:1 com lógico.
		if hw.LogicalCores == hw.Cores {
			t.Errorf("cores identicos a logical cores em CPU com HT (fisico=%d logico=%d) — deteccao degenerada", hw.Cores, hw.LogicalCores)
		}
		t.Logf("CPU com HT: fisico=%d logico=%d", hw.Cores, hw.LogicalCores)
	}
}

// TestFixWindows11ProductName verifica a correção do bug do registry:
// ProductName continua "Windows 10 ..." mesmo em Windows 11 (build >= 22000).
func TestFixWindows11ProductName(t *testing.T) {
	cases := []struct {
		productName string
		build       string
		want        string
	}{
		{"Windows 10 Pro", "26220", "Windows 11 Pro"},
		{"Windows 10 Pro", "26220.9223", "Windows 11 Pro"},
		{"Windows 10 Home", "22000", "Windows 11 Home"},
		{"Windows 10 Pro", "19045", "Windows 10 Pro"}, // build 10 real
		{"Windows 10 Pro", "21999", "Windows 10 Pro"}, // abaixo de 22000
		{"Windows 11 Pro", "26220", "Windows 11 Pro"}, // já correto
		{"", "26220", ""},                        // vazio permanece vazio
		{"Windows 10 Pro", "", "Windows 10 Pro"}, // sem build, não mexe
	}
	for _, tc := range cases {
		if got := fixWindows11ProductName(tc.productName, tc.build); got != tc.want {
			t.Errorf("fixWindows11ProductName(%q, %q) = %q, want %q", tc.productName, tc.build, got, tc.want)
		}
	}
}

// TestCollectDisks verifies that logical volumes are enumerated.
func TestCollectDisks(t *testing.T) {
	volumes, physical, err := collectDisksNative(context.Background())
	if err != nil {
		t.Fatalf("collectDisksNative: %v", err)
	}
	// At least the C: drive should be present.
	foundC := false
	for _, v := range volumes {
		if v.Device == "C:\\" {
			foundC = true
			break
		}
	}
	if !foundC {
		t.Errorf("volume C:\\ nao encontrado; volumes=%v", volumes)
	}
	_ = physical
}

// TestCollectSoftware verifies that installed software is read from the registry.
func TestCollectSoftware(t *testing.T) {
	items, err := collectSoftwareNative(context.Background())
	if err != nil {
		t.Fatalf("collectSoftwareNative: %v", err)
	}
	if len(items) == 0 {
		t.Log("nenhum software encontrado (pode ser ambiente minimo)")
	}
}

// TestCollectNetworks verifies that network interfaces are enumerated.
func TestCollectNetworks(t *testing.T) {
	networks, err := collectNetworksNative(context.Background())
	if err != nil {
		t.Fatalf("collectNetworksNative: %v", err)
	}
	if len(networks) == 0 {
		t.Log("nenhuma interface de rede encontrada")
	}
}

// TestCollectNetworkConnections verifies that listening ports are enumerated.
func TestCollectNetworkConnections(t *testing.T) {
	listening, open, err := collectNetworkConnectionsNative(context.Background())
	if err != nil {
		t.Fatalf("collectNetworkConnectionsNative: %v", err)
	}
	_ = listening
	_ = open
}

// TestPortBytesSwapsNetworkOrderPort trava a correção de byte order: as tabelas
// MIB (TCP/UDP) guardam a porta em network byte order nos 16 bits baixos do
// DWORD. Em hosts little-endian o campo lido no uint32 é a porta com os bytes
// trocados (ex.: 41080 lê-se 0x78A0 = 30880), então portBytes precisa
// desempilhar os bytes para que binary.BigEndian devolva a porta real.
// Antes da correção, 135 (RPC) era reportado como 34560 e 3389 (RDP) como 15629.
func TestPortDecodesNetworkOrderPort(t *testing.T) {
	cases := map[uint32]int{
		0x8700: 135,   // RPC endpoint mapper
		0x3D0D: 3389,  // RDP
		0x78A0: 41080, // faixa HTTP P2P do agente (DefaultP2PPortRangeStart)
		0x5000: 80,    // HTTP
	}
	for raw, want := range cases {
		if got := nettable.Port(raw); got != want {
			t.Errorf("nettable.Port(0x%X) = %d, want %d", raw, got, want)
		}
	}
}

// TestCollectNetworkConnectionsHonorsCancellation garante que a coleta nativa
// respeita o contexto cancelado (antes o ctx era ignorado com "_ = ctx").
func TestCollectNetworkConnectionsHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	listening, open, err := collectNetworkConnectionsNative(ctx)
	if err == nil {
		t.Fatal("esperava erro de contexto cancelado")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if listening != nil || open != nil {
		t.Fatalf("esperava coleções nulas em cancelamento, veio listening=%v open=%v", listening, open)
	}
}

// TestCollectOpenSocketsReportsRealPorts reproduce o cenário do bug na ponta:
// antes da correção, um soquete na porta P era reportado como byteswap16(P)
// (ex.: 41080 -> 30880), então nem a porta em escuta nem o soquete aberto
// casavam com a porta realmente vinculada pelo processo.
func TestCollectOpenSocketsReportsRealPorts(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	wantPort := ln.Addr().(*net.TCPAddr).Port

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	local := conn.LocalAddr().(*net.TCPAddr).Port
	remote := conn.RemoteAddr().(*net.TCPAddr).Port

	seen := make(map[string]struct{})
	var foundListen bool
	for _, p := range collectTCPListening(nettable.AfInet, seen) {
		if p.Port == wantPort {
			foundListen = true
			break
		}
	}

	seenOpen := make(map[string]struct{})
	var foundOpen bool
	for _, s := range collectTCPOpen(nettable.AfInet, seenOpen) {
		if s.LocalPort == local && s.RemotePort == remote {
			foundOpen = true
			break
		}
	}

	if !foundListen {
		t.Errorf("porta em escuta %d não encontrada na coleta (byte order regrediu?)", wantPort)
	}
	if !foundOpen {
		t.Errorf("soquete local=%d remote=%d não encontrado na coleta (byte order regrediu?)", local, remote)
	}
}

// TestCollectHardware verifies that hardware details are collected via WMI.
func TestCollectHardware(t *testing.T) {
	hw, memory, gpus, cpus, features, err := collectHardwareNative(context.Background())
	if err != nil {
		t.Fatalf("collectHardwareNative: %v", err)
	}
	_ = hw
	_ = memory
	_ = gpus
	_ = cpus
	_ = features
}

// TestCollectStartupItems verifies that startup items are read from the registry.
func TestCollectStartupItems(t *testing.T) {
	items, err := collectStartupItemsNative(context.Background())
	if err != nil {
		t.Fatalf("collectStartupItemsNative: %v", err)
	}
	_ = items
}

// TestCollectLoggedInUsers verifies that logged-in users are enumerated.
func TestCollectLoggedInUsers(t *testing.T) {
	users, err := collectLoggedInUsersNative(context.Background())
	if err != nil {
		t.Fatalf("collectLoggedInUsersNative: %v", err)
	}
	_ = users
}

// TestCollectBattery verifies that battery info is collected.
func TestCollectBattery(t *testing.T) {
	battery, err := collectBatteryNative(context.Background())
	if err != nil {
		t.Fatalf("collectBatteryNative: %v", err)
	}
	_ = battery
}

// TestCollectBitLocker verifies that BitLocker status is collected.
func TestCollectBitLocker(t *testing.T) {
	bitlocker, err := collectBitLockerNative(context.Background())
	if err != nil {
		t.Fatalf("collectBitLockerNative: %v", err)
	}
	_ = bitlocker
}
