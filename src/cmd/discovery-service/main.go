//go:build windows

// discovery-service — binário do serviço Windows do Discovery Agent
// (PLANO_SEPARACAO_SERVICO_UI.md, Fase B — decisão D1: dois binários).
//
// Este executável é instalado pelo NSIS como serviço `DiscoveryAgent`
// (LocalSystem, start= auto). Ele roda o core do agent (agentConn/NATS,
// inventory, sync, automation, P2P, self-update, outboxes) e NUNCA inicializa
// a UI Wails: sem janela, sem tray, sem WebView2, sem frontend embedado.
//
// A UI do usuário é o binário discovery-agent.exe (sessão interativa), que
// conecta a este serviço via IPC named pipe (modo companion).
//
// Aceita as flags:
//   --service                modo serviço explícito (auto-detectado pelo SCM também)
//   --remote-session-worker  modo worker de remote session (spawnado pelo
//                            próprio serviço na sessão interativa — NUNCA
//                            rodar o core nesses processos)
//   --terminal-dispatcher    dispatcher do terminal (ConPTY isolado em processo
//                            filho, spawnado pelo core/worker — nunca rodar o
//                            core nesses processos)
//   (sem flag)               roda o core direto (debug/manual, fora do SCM)

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	// Mesma razão do discovery-agent: tzdata embutido para time.LoadLocation
	// funcionar em Windows sem Go (janela de manutenção com timeZone IANA).
	_ "time/tzdata"

	appkg "discovery/app"
	"discovery/app/core/memlimit"
	"discovery/app/core/platform"
	"discovery/app/core/tpmvault"
	"discovery/app/services/hardwareid"
)

// hasStartupArg replica a detecção de flags do main.go da UI (case-insensitive,
// trim de espaços — mesmas regras de parsing dos dois binários).
func hasStartupArg(arg string) bool {
	for _, value := range os.Args[1:] {
		if strings.EqualFold(strings.TrimSpace(value), arg) {
			return true
		}
	}
	return false
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)

	// ── Modo worker de remote session (PLANO_AGENT_SERVICE_SYSTEM.md §7.2) ──
	// O serviço spawna ESTE executável com --remote-session-worker na sessão
	// interativa (CreateProcessAsUser com lpDesktop winsta0\winlogon quando
	// não há usuário logado). O worker roda na SESSÃO INTERATIVA como SYSTEM:
	// captura a tela REAL da máquina — inclusive o desktop Winlogon ANTES do
	// login (o input desktop é seguido por CheckDesktopSwitch a cada iteração,
	// padrão MeshAgent kvm.c) — e injeta input em todas as janelas.
	//
	// FIX 2026-09-13 (acesso remoto não conecta): este dispatch NÃO existia no
	// binário do serviço — a flag só era tratada no main.go da UI. O processo
	// spawnado bootava uma SEGUNDA instância completa do serviço (core, NATS,
	// sync) em vez do worker: nenhum frame era publicado, o spawn reportava
	// exitCode=0 falso e as duas instâncias disputavam config/SQLite/NATS.
	if hasStartupArg("--remote-session-worker") || hasStartupArg("/remote-session-worker") || hasStartupArg("-remote-session-worker") {
		appkg.RunRemoteSessionWorker()
		return
	}

	// ── Modo dispatcher do terminal (ConPTY isolado em processo filho) ──
	// Spawnado pelo core/worker quando o dispatcher está habilitado
	// (padrão a partir de 13/09/2026). Sem este dispatch, o processo filho
	// bootaria OUTRA instância completa do serviço (mesma classe de bug do
	// --remote-session-worker) e o terminal nunca iniciaria.
	if hasStartupArg("--terminal-dispatcher") || hasStartupArg("/terminal-dispatcher") || hasStartupArg("-terminal-dispatcher") {
		appkg.TerminalRunDispatcher()
		return
	}

	// ── Diagnóstico/limpeza do cofre TPM (token+URL) ──
	// --tpm-vault-status: NVReadPublic funciona sem elevação; se a leitura
	// falhar (ex.: TPM_E_COMMAND_BLOCKED) imprime o erro exato e sai com 1.
	// --tpm-vault-clear: NVUndefineSpace exige elevação — fail-closed, no
	// mesmo padrão de --agent-delete-cleanup.
	if hasStartupArg("--tpm-vault-status") || hasStartupArg("/tpm-vault-status") || hasStartupArg("-tpm-vault-status") {
		runTpmVaultStatus()
		return
	}
	if hasStartupArg("--tpm-vault-clear") || hasStartupArg("/tpm-vault-clear") || hasStartupArg("-tpm-vault-clear") {
		runTpmVaultClear()
		return
	}

	// Teto suave de memória do runtime Go (default 192 MiB; override por
	// DISCOVERY_MEMORY_LIMIT_MB, 0 desabilita). Só para o processo do SERVIÇO/
	// core: os modos auxiliares acima (worker de sessão remota, dispatcher do
	// terminal) retornam antes de chegar aqui de propósito — o worker de sessão
	// remota aloca frames BGRA inteiros (33 MB em 4K, 66 MB em 4K HDR) no pool de
	// captura, e um teto suave apertado ali viraria GC thrash durante o streaming.
	if mib := memlimit.Apply(192); mib > 0 {
		log.Printf("[startup] teto de memória do runtime: %d MiB", mib)
	}

	appkg.RunServiceMode()
}

// openTpmVaultForCLI abre o cofre TPM com a identidade de hardware (EK/SMBIOS).
// Enabled=true é o kill switch OPERACIONAL (a política do servidor governa
// gravar/apagar na camada de serviço, nunca a leitura de boot).
func openTpmVaultForCLI() tpmvault.Vault {
	return tpmvault.Open(tpmvault.Options{
		Logf: func(line string) {
			log.Printf("%s", line)
		},
		Fingerprint: func() (string, string) {
			hw := hardwareid.New(hardwareid.Deps{
				Logf: func(line string) { log.Printf("%s", line) },
			}).Get()
			return hw.TPMEK, hw.SMBIOSUUID
		},
		Enabled: true,
	})
}

// runTpmVaultStatus imprime o status MASCARADO do cofre (sem token) como JSON.
// Sem registro = exit 0. Falha de leitura (TPM indisponível/comando bloqueado)
// imprime o erro exato e sai com 1 para não mascarar o diagnóstico.
func runTpmVaultStatus() {
	v := openTpmVaultForCLI()
	st := v.Status()
	rec, ok, err := v.Load()
	if err == nil {
		st.Present = ok
		if ok {
			st.Host = rec.Host()
			st.TokenPrefix = tpmvault.TokenPrefix(rec.Token())
			st.AllowInsecureTLS = rec.AllowInsecureTLS
		}
	} else if strings.TrimSpace(st.LastError) == "" {
		st.LastError = err.Error()
	}
	data, marshalErr := json.MarshalIndent(st, "", "  ")
	if marshalErr != nil {
		fmt.Fprintf(os.Stderr, "tpm-vault-status: %v\n", marshalErr)
		os.Exit(1)
	}
	fmt.Println(string(data))
	if err != nil {
		fmt.Fprintf(os.Stderr, "tpm-vault-status: %v\n", err)
		os.Exit(1)
	}
}

// runTpmVaultClear apaga o registro do cofre. Fail-closed: NVUndefineSpace é
// bloqueado pelo TBS sem elevação, então sem token elevado sai com 1.
func runTpmVaultClear() {
	if !platform.IsElevated() {
		fmt.Fprintln(os.Stderr, "tpm-vault-clear: requer execucao elevada (admin/SYSTEM) para apagar o cofre TPM")
		os.Exit(1)
	}
	v := openTpmVaultForCLI()
	if err := v.Erase(); err != nil {
		fmt.Fprintf(os.Stderr, "tpm-vault-clear: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("cofre TPM apagado")
}
