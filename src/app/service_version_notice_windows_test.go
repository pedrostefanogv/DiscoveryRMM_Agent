//go:build windows

package app

import (
	"encoding/json"
	"strings"
	"testing"

	"discovery/app/core/buildinfo"
)

// Contrato JSON do binding GetIPCServiceInfo (A.2.2), consumido por
// frontend/js/app-init.js. O aviso só pode aparecer quando o handshake IPC já
// aconteceu (connected=true), o serviço informou versão e ela difere da UI.
func TestGetIPCServiceInfo_JSONContract(t *testing.T) {
	type serviceInfo struct {
		UIVersion      string `json:"uiVersion"`
		ServiceVersion string `json:"serviceVersion"`
		Protocol       int    `json:"protocol"`
		Connected      bool   `json:"connected"`
		Mismatch       bool   `json:"mismatch"`
	}
	decode := func(t *testing.T, raw string) serviceInfo {
		t.Helper()
		var info serviceInfo
		if err := json.Unmarshal([]byte(raw), &info); err != nil {
			t.Fatalf("GetIPCServiceInfo devolveu JSON inválido (%v): %q", err, raw)
		}
		if info.UIVersion != strings.TrimSpace(buildinfo.Version) {
			t.Fatalf("uiVersion = %q, esperado %q", info.UIVersion, buildinfo.Version)
		}
		return info
	}

	// Standalone (sem ipcClient): sem serviço, sem aviso.
	t.Run("standalone", func(t *testing.T) {
		raw, err := (&App{}).GetIPCServiceInfo()
		if err != nil {
			t.Fatalf("GetIPCServiceInfo: %v", err)
		}
		info := decode(t, raw)
		if info.Connected || info.Mismatch {
			t.Fatalf("standalone não pode reportar connected/mismatch: %+v", info)
		}
	})

	// Companion com o MESMO par: conectado, mas sem aviso.
	t.Run("mesma-versao", func(t *testing.T) {
		client := NewIPCClient(nil, nil)
		client.serviceVersion = strings.TrimSpace(buildinfo.Version)
		client.serviceProtocol = IPCProtocolVersion
		raw, err := (&App{ipcClient: client}).GetIPCServiceInfo()
		if err != nil {
			t.Fatalf("GetIPCServiceInfo: %v", err)
		}
		info := decode(t, raw)
		if !info.Connected {
			t.Fatal("com handshake concluído connected deve ser true")
		}
		if info.Mismatch {
			t.Fatalf("mesma versão não pode gerar mismatch: %+v", info)
		}
		if info.Protocol != IPCProtocolVersion {
			t.Fatalf("protocol = %d, esperado %d", info.Protocol, IPCProtocolVersion)
		}
	})

	// Par misto: connected=true e mismatch=true (é o que dispara o toast).
	t.Run("par-misto", func(t *testing.T) {
		uiVersion := strings.TrimSpace(buildinfo.Version)
		if uiVersion == "" {
			t.Skip("buildinfo.Version vazio: o mismatch exige as duas versões preenchidas")
		}
		client := NewIPCClient(nil, nil)
		client.serviceVersion = uiVersion + "-outra-build"
		client.serviceProtocol = IPCProtocolVersion
		raw, err := (&App{ipcClient: client}).GetIPCServiceInfo()
		if err != nil {
			t.Fatalf("GetIPCServiceInfo: %v", err)
		}
		info := decode(t, raw)
		if !info.Connected || !info.Mismatch {
			t.Fatalf("par misto deve reportar connected=true/mismatch=true: %+v", info)
		}
		if info.ServiceVersion != client.serviceVersion {
			t.Fatalf("serviceVersion = %q, esperado %q", info.ServiceVersion, client.serviceVersion)
		}
	})
}
