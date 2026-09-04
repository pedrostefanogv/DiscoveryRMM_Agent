# Plano: Agent como Serviço Windows (SYSTEM) + UI Companion no Usuário

> Status: **REVISADO 2026-09-04 — plano se aplica ao código atual; ajustes incorporados e marcados com ⚠️**
> Data: 2026-08-30 (revisão: 2026-09-04)
> Escopo: instalação e execução do Discovery Agent em dois processos:
>
> 1. **Serviço Windows** rodando como `LocalSystem` (ativo desde o boot, sem login)
> 2. **UI Companion** rodando na sessão do usuário logado (tray + janela + interação)

---

## 1. Objetivo

Hoje o agent só executa via Scheduled Task `DiscoveryAgentUI` disparada **no logon** do usuário. Isso significa:

- Antes do primeiro login, a máquina está **sem agente** (sem heartbeat, sem inventário, sem comandos remotos, sem P2P, sem automação).
- Se nenhum usuário fizer login, o agent nunca sobe.

O objetivo é garantir que o **core do agent** (conexão NATS, comandos, inventário, automação, P2P, self-update) rode **desde o boot da máquina** como serviço `LocalSystem`, enquanto a **interface** (janela Wails, tray, chat, notificações interativas) roda **na sessão do usuário** para interação.

## 2. Arquitetura Proposta

```
┌─────────────────────────────────────────────────────────────────┐
│ Boot do Windows (sem usuário logado)                            │
│   SCM inicia: DiscoveryAgent (LocalSystem, auto, delayed)        │
│   discovery-agent.exe --service                                 │
│   ├─ agentConn (NATS) ✓                                         │
│   ├─ inventory + sync ✓                                        │
│   ├─ automation ✓                                              │
│   ├─ P2P coordinator ✓                                         │
│   ├─ self-update ✓                                              │
│   ├─ IPC server (named pipe \\.\pipe\discovery-agent-ipc) ✓      │
│   └─ notificações → fallback headless (persiste, sem UI)         │
└─────────────────────────────────────────────────────────────────┘
                              │ IPC (status, eventos, comandos UI)
┌─────────────────────────────────────────────────────────────────┐
│ Logon do usuário                                                │
│   Scheduled Task DiscoveryAgentUI (existente)                  │
│   discovery-agent.exe --startup-minimized --startup-source=...  │
│   ├─ handshake IPC: serviço ativo?                              │
│   │    SIM  → modo companion: só UI/tray/chat/notificações      │
│   │    NÃO → modo standalone: core completo na UI (fallback)    │
│   └─ janela Wails + tray + interação do usuário                 │
└─────────────────────────────────────────────────────────────────┘
```

### 2.1 Por que binário único (e não dois .exe)

- O projeto já usa o padrão "modo por argumento" (`--terminal-dispatcher`, `--agent-delete-cleanup`) no `main.go`.
- Evita duplicar build, assinatura, self-update e instalador.
- O custo é o tamanho do binário carregado no serviço (frontend embedado ~ alguns MB) — aceitável.

### 2.2 Divisão de responsabilidades

| Componente                       | Processo serviço (SYSTEM)    | Processo UI (usuário)                |
| -------------------------------- | ---------------------------- | ------------------------------------ |
| agentConn (NATS/heartbeat)       | **Sim (dono)**               | Não (companion)                      |
| HandleCommand (comandos remotos) | **Sim (dono)**               | Recebe via IPC quando interativo     |
| Inventory + sync                 | **Sim (dono)**               | Consulta via IPC                     |
| Automation (PSADT/scripts)       | **Sim (dono)**               | Não                                  |
| P2P                              | **Sim (dono)**               | Não                                  |
| Self-update                      | **Sim (dono)**               | Não                                  |
| Notificações                     | Persiste + fallback headless | **Renderiza** (toast/modal/banner)   |
| Chat AI                          | Não                          | **Sim** (precisa UI)                 |
| Remote session / terminal        | Não                          | **Sim** (precisa sessão)             |
| Tray / janela                    | Não                          | **Sim**                              |
| SQLite (ProgramData)             | Leitura/escrita              | Leitura/escrita (WAL + busy_timeout) |

## 3. Fases de Implementação

### Fase 0 — Pré-requisitos (pequenas correções)

1. **SQLite multi-processo**: adicionar `busy_timeout=5000` em `database.Open()` (`src/app/core/database/sqlite.go`). Hoje serviço e UI vão abrir o mesmo `discovery.db` em `C:\ProgramData\Discovery` — sem busy_timeout, `SQLITE_BUSY` vira erro intermitente.
   - ⚠️ **Ajuste (revisão)**: `conn.Exec("PRAGMA busy_timeout=5000")` não é garantido no pool do `database/sql` — a pragma aplica-se a uma conexão e pode se perder no recycle. Com `modernc.org/sqlite`, preferir pragmas via DSN: `sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)")` e migrar `journal_mode=WAL`/`synchronous=NORMAL` para o DSN também. Verificado: hoje só existem `journal_mode=WAL`, `synchronous=NORMAL`, `cache_size=-64000` via `Exec` (sqlite.go:172-174), sem busy_timeout.
   - ⚠️ Nota: `cache_size=-64000` (64MB) por processo × 2 processos = 128MB só de cache — considerar reduzir no serviço (ex.: `-8000`).
2. **Log do serviço**: garantir que `logger` escreve em `%ProgramData%\Discovery\logs\agent-service.log` quando em modo serviço (hoje `LogFilePath()` já aponta para `agent.log`; separar arquivo por modo facilita diagnóstico). Verificado: `logger.SetFileOutput()` (core/logger/logger.go) já existe e aceita path arbitrário — basta chamar com o path do serviço no modo `--service`.
3. Confirmar que `EmitEvent`/`DispatchNotification` já toleram ausência de UI (verificado: `headless_no_context` já existe em `services/notifications/service.go` — apenas garantir que o serviço injeta `ctx` não-nil mas sem `EmitEvent` real, caindo no caminho headless).

### Fase 1 — Modo serviço no binário (`--service`)

**Arquivos novos:**

- `src/app/servicemode/service_windows.go` — implementação `svc.Handler`:

  ```go
  type discoveryService struct{ app *app.CoreAgent }
  func (s *discoveryService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32)
  ```

  - Aceita `Start`, `Stop`, `Shutdown`, `Interrogate` (⚠️ o campo é `svc.Status.Accepts`, não `Accepted`: `changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}`).
  - No `Start`: monta o **core do agent** (sem Wails) e roda o mesmo pipeline de `startup()` — porém **sem** tray, sem janela, sem debug HTTP de UI.

- `src/app/servicemode/service_other.go` — stub não-Windows.
- `src/app/servicemode/ipc_windows.go` — servidor named pipe `\\.\pipe\discovery-agent-ipc` (DACL permitindo Users + SYSTEM; padrão já usado no projeto em `core/terminal/dispatcher_windows.go`).
  - ⚠️ **Ajuste (revisão)**: o padrão atual do projeto usa `winio.ListenPipe(path, nil)` com DACL `nil` (dispatcher_windows.go:66-72) — default restrito, **não** serve para serviço→UI. O pipe do serviço precisa de SDDL explícito (ex.: `winio.Sddl` com `D:(A;;GA;;;SY)(A;;GA;;;BU)` para SYSTEM + Users builtin), senão a UI do usuário não conecta.

**Refactor necessário (o ponto mais delicado do plano):**

- `App.startup()` (⚠️ linha atual: `src/app/app.go:1097`) hoje mistura core + UI. Extrair para `CoreAgent` reutilizável:
  - `agentConn.Run`, `inventorySvc`, `automationSvc`, `syncSvc`, `p2pCoord`, `selfUpdater`, outboxes, consolidation engine, DB — movidos para um struct `CoreAgent` com método `Run(ctx)`.
  - ⚠️ **Preservar o staged startup** (revisão): `startup()` usa fases com delays — inventory +2s, agentConn +8s, automation/sync/P2P +10s, self-update/cleanup +12s — e guards como `isInventoryProvisioned()`/`beginActivity()`. A extração deve levar os delays junto do core, senão o serviço satura CPUs modestas no boot.
  - ⚠️ Itens UI-coupled que ficam na `App`: `captureStdLog`, `EnsureChatSSEServer`, `startTray`, `hideWindowOnStartup`, `applyIdleMode`, `StartDebugHTTPServer` (debug de UI).
  - `App` (UI) passa a **compor** `CoreAgent` quando standalone, ou **conectar via IPC** quando companion.
- Alternativa mais conservadora (se revisão preferir): manter `App` intacto e criar flag `runtimeFlags.ServiceMode` que pula `startTray`, `hideWindowOnStartup`, `EnsureChatSSEServer`, `StartDebugHTTPServer` e o registro Wails. **Menos código, mas acoplamento permanece** — recomendo a extração.

**`main.go`:**

```go
if hasStartupArg("--service") || isWindowsService() { // svc.IsWindowsService()
    appkg.RunServiceMode() // nunca inicializa Wails application
    return
}
```

- `svc.IsWindowsService()` detecta quando o SCM é o parent (robusto contra alguém rodando `--service` manualmente).

### Fase 2 — IPC serviço ↔ UI (named pipe)

Contrato mínimo (JSON lines sobre pipe duplex):

| Mensagem               | Direção  | Uso                                                                |
| ---------------------- | -------- | ------------------------------------------------------------------ |
| `hello` / `hello_ack`  | UI → svc | Handshake: UI pergunta "serviço ativo?" → define modo companion    |
| `status`               | UI → svc | Snapshot (conectividade, agentId, versão) para `GetStatusOverview` |
| `event`                | svc → UI | Repasse de eventos (connectivity, notification:new, chat:question) |
| `notification:respond` | UI → svc | Resposta do usuário (approve/deny) para `require_confirmation`     |
| `command_result`       | UI → svc | Resultado de comando interativo executado na sessão                |

- **Notificações**: serviço recebe comando NATS `ShowPsadtAlert`/notification → publica `event` no pipe → UI renderiza (toast/modal PSADT) → UI responde `notification:respond` → serviço publica resultado no NATS. Sem UI conectada → caminho headless atual (persist + `timeout_policy_applied`).
- **Chat/remote session**: permanecem 100% na UI (exigem sessão interativa). O serviço apenas marca no heartbeat que a UI está offline (campo opcional `uiOnline` no payload de heartbeat — extensão futura, não bloqueante).
- Reconnect automático do pipe com backoff (padrão já usado em `agentconn`).

### Fase 3 — Instalação via NSIS

**`project.nsi` — nova função `RegisterAgentService`:**

```nsis
Function RegisterAgentService
   ; 1. Parar taskkill de instâncias em execução (PrepareForInPlaceUpdate já faz)
   ; 2. Criar serviço
   nsExec::ExecToLog /OEM '"$SYSDIR\sc.exe" create DiscoveryAgent binPath= "\"$INSTDIR\${PRODUCT_EXECUTABLE}\" --service" start= delayed-auto obj= LocalSystem DisplayName= "Discovery Agent Service"'
   ; 3. Recuperação de falha (crash → restart)
   nsExec::ExecToLog /OEM '"$SYSDIR\sc.exe" failure DiscoveryAgent reset= 86400 actions= restart/5000/restart/5000/restart/5000'
   ; 4. Descrição
   nsExec::ExecToLog /OEM '"$SYSDIR\sc.exe" description DiscoveryAgent "Discovery Agent core service (NATS, inventory, automation, P2P)"'
   ; 5. Iniciar agora (não esperar reboot)
   nsExec::ExecToLog /OEM '"$SYSDIR\sc.exe" start DiscoveryAgent'
FunctionEnd
```

- `delayed-auto`: evita competir com serviços críticos no boot; o agent reconecta NATS com jitter infinito, então 30-60s de atraso pós-boot é aceitável.
- **Uninstall**: `un.UnregisterAgentService` — `sc.exe stop` + `sc.exe delete` antes do decommission.
- **Update (`/UPDATE`)**: `PrepareForInPlaceUpdate` precisa também parar o serviço — ⚠️ **ordem obrigatória (revisão)**: `sc stop DiscoveryAgent` (desarma o failure recovery) → aguardar STOPPED → taskkill residual → rename `.bak_update`. Se o taskkill matar o processo do serviço sem `sc stop` antes, o SCM entende como crash e as failure actions reiniciam o serviço em ~5s, correndo no meio da cópia dos binários (race). Nota: o `taskkill /IM discovery-agent.exe` mata serviço E UI (mesmo exe) — esperado no update.
  - ⚠️ **Ajuste (revisão)**: `sc stop` retorna antes do serviço estar efetivamente STOPPED. O NSIS precisa de loop de espera (`sc query DiscoveryAgent` até `STOPPED`, com timeout ~30s) entre o stop e o taskkill/rename — senão o rename `.bak_update` falha com o .exe ainda carregado. O helper Go `sysctrl.StopService` (services_windows.go:193) também não aguarda; se usado pelo serviço para self-stop, adicionar wait de estado.
- Ao final do update: `sc start DiscoveryAgent` **e** manter o `Exec` da UI companion com `--startup-minimized --startup-source=update-restart` (o `Exec` cobre o fallback standalone quando o serviço não existe).
- Mantém a Scheduled Task `DiscoveryAgentUI` existente (agora ela só sobe a UI companion).

### Fase 4 — Self-update ciente do serviço

- `selfupdate/launch_windows.go`: quando em modo serviço (SYSTEM), o instalador `/S /UPDATE` já roda elevado — sem prompt UAC. Ajustar:
  - ⚠️ **Ajuste (revisão)**: verificado em `core/selfupdate/launch_windows.go` — `isProcessElevated()` (linha 334) já existe e o caminho `CreateProcess` com `CREATE_BREAKAWAY_FROM_JOB` (linha 142) já cobre o serviço SYSTEM. **Porém**, garantir que o serviço NUNCA caia no caminho `ShellExecuteEx("runas")` (`LaunchInstallerElevated`, linha 76): em sessão 0 não há desktop para o prompt UAC — o `runas` falha ou pende. O serviço SYSTEM é sempre elevado, então o caminho `CreateProcess` breakaway é o correto; basta assegurar que `isProcessElevated()` retorna `true` para SYSTEM (token de integridade System) e que o fallback `launchInstallerShellExecute` não é alcançado no modo serviço.
  - `PrepareForInPlaceUpdate` para/sobe o serviço (Fase 3).
  - Após update, o serviço reinicia a si mesmo via SCM; a UI companion detecta pipe caindo e reconecta (Fase 2 já cobre).
- `ResumePendingInstallReport` inalterado (correlação com installer.log continua válida).

### Fase 5 — Testes e validação

1. **Unitários**: contrato IPC (marshal/unmarshal), modo companion vs standalone (decisão por handshake), busy_timeout SQLite.
2. **Integração manual (checklist)**:
   - Instalar → reboot **sem login** → verificar heartbeat/inventário no servidor.
   - Login → UI companion sobe, tray visível, notificação `require_confirmation` flui serviço→UI→resposta.
   - Matar serviço → SCM reinicia em 5s (failure actions).
   - Self-update com serviço ativo → serviço para, binário troca, serviço volta.
   - Uninstall limpa serviço + task + ProgramData.
3. **Regressão**: modo standalone (serviço desativado manualmente) deve se comportar como hoje.
4. ⚠️ **Inventário como SYSTEM vs usuário** (revisão): coletas rodando como `LocalSystem` podem divergir das atuais (sessão de usuário elevado) — ex.: apps per-user, contexto WMI. Comparar um inventário coletado pelo serviço com um coletado pela UI standalone e validar o delta aceitável no servidor.

## 4. Riscos e Mitigações

| Risco                                                  | Impacto  | Mitigação                                                                                                                                                                                                   |
| ------------------------------------------------------ | -------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Refactor de `App.startup()` quebrar UI                 | Alto     | Extração incremental com testes `go test ./app/...` após cada passo; manter `App` como wrapper de `CoreAgent`                                                                                               |
| Comandos duplicados (UI standalone + serviço)          | Médio    | Handshake IPC define dono único do core; UI só assume core se serviço ausente por N segundos                                                                                                                |
| `SQLITE_BUSY` entre processos                          | Médio    | `busy_timeout=5000` + WAL (já ativo)                                                                                                                                                                        |
| Notificações `require_confirmation` sem usuário logado | Médio    | Caminho headless já existe (`timeout_policy_applied`); documentar comportamento                                                                                                                             |
| Remote session/terminal não funcionam sem login        | Esperado | Por design (exige sessão interativa); opcionalmente serviço pode lançar UI via `CreateProcessAsUser` (fase futura, fora do escopo)                                                                          |
| `mgr.Connect` com SC_MANAGER_ALL_ACCESS no serviço     | Nenhum   | Serviço roda como SYSTEM — acesso garantido (verificado: `StartService`/`StopService` em sysctrl usam `mgr.Connect`; `ListServices` já usa direitos mínimos)                                                |
| Manifest `requireAdministrator` vs SCM                 | Nenhum   | SCM ignora `requestedExecutionLevel` ao lançar serviços                                                                                                                                                     |
| SingleInstance Wails `com.discovery.app`               | Nenhum   | Serviço nunca cria `application.New`                                                                                                                                                                        |
| `ShellExecuteEx("runas")` na sessão 0 (serviço)        | Alto     | Serviço SYSTEM sempre elevado → caminho `CreateProcess` breakaway (launch_windows.go:142); garantir que `isProcessElevated()` = true para SYSTEM e que o fallback `runas` nunca é alcançado no modo serviço |
| Pipe IPC com DACL default                              | Médio    | `winio.ListenPipe(path, nil)` restringe acesso; usar SDDL explícito `(A;;GA;;;SY)(A;;GA;;;BU)` no pipe do serviço                                                                                           |
| `sc stop` sem aguardar STOPPED                         | Médio    | Loop `sc query` até STOPPED (timeout 30s) antes de taskkill/rename; `sysctrl.StopService` também não aguarda                                                                                                |

## 4.1 Resultado da revisão contra o código (2026-09-04)

**Verificado — plano se aplica:**

| Afirmação do plano                       | Verificação no código                                                                                                       |
| ---------------------------------------- | --------------------------------------------------------------------------------------------------------------------------- |
| `busy_timeout` ausente                   | ✅ `sqlite.go:171-174` — só WAL/synchronous/cache_size via `Exec`                                                           |
| Caminho headless de notificações         | ✅ `services/notifications/service.go:256-260` (`headless_no_context`/`headless_logged`/`timeout_policy_applied`)           |
| Padrão "modo por argumento" no `main.go` | ✅ `--agent-delete-cleanup`, `--terminal-dispatcher` (main.go:57-84)                                                        |
| `svc`/`mgr` já usados no projeto         | ✅ `core/sysctrl/services_windows.go`                                                                                       |
| Named pipes já usados (padrão IPC)       | ✅ `core/terminal/dispatcher_windows.go`                                                                                    |
| NSIS sem service mode hoje               | ✅ `project.nsi:658` ("Nao usamos mais Windows Service mode"), `RegisterUIStartupTask:1154`, `PrepareForInPlaceUpdate:1075` |
| `EmitEvent` no-op sem UI                 | ✅ `app.go:876/909` (`a.app == nil`)                                                                                        |
| Manifest `requireAdministrator`          | ✅ SCM ignora `requestedExecutionLevel` ao lançar serviços                                                                  |

**Ajustes incorporados nesta revisão (⚠️ no texto):**

1. Fase 0.1: pragmas via DSN (não `conn.Exec`) — garantia em todas as conexões do pool.
2. Fase 1: linha de `startup()` corrigida para 1097; staged startup deve ser preservado na extração `CoreAgent`.
3. Fase 1: campo `svc.Status.Accepts` (não `Accepted`).
4. Fase 2: pipe do serviço exige SDDL explícito — o padrão `winio.ListenPipe(path, nil)` do projeto (DACL default) não permite conexão da UI do usuário.
5. Fase 3: ordem `sc stop` → aguardar STOPPED (loop `sc query`, timeout 30s) → taskkill no update (evita race com failure recovery do SCM e rename com .exe carregado).
6. Fase 4: serviço SYSTEM deve usar sempre o caminho `CreateProcess` breakaway (`launch_windows.go:142`); `ShellExecuteEx("runas")` falha na sessão 0 (sem desktop para UAC).
7. Fase 5: validação de inventário SYSTEM vs usuário (divergência de apps per-user/WMI).

**Verificações adicionais (Fases 2 e 4):**

| Afirmação do plano                     | Verificação no código                                                                                                                            |
| -------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| Named pipes via `winio` (padrão IPC)   | ✅ `dispatcher_windows.go:66-72`, `pty_windows_dispatcher_client.go` — mas com DACL `nil` (ver ajuste 4)                                         |
| Self-update elevado sem UAC no serviço | ✅ `isProcessElevated()` (launch_windows.go:334) + `CreateProcess` breakaway (linha 142) já existem; risco apenas no fallback `runas` (ajuste 6) |
| `mgr.Connect` no sysctrl               | ✅ `StartService`/`StopService` usam `mgr.Connect` (services_windows.go:173/193); `ListServices` já corrigido para direitos mínimos              |

## 5. Pontos em aberto para decisão do revisor

1. **Extração `CoreAgent` vs flag `ServiceMode`**: recomendo extração (limpa, testável), mas é o maior volume de código. Flag é mais rápida mas mantém acoplamento.
2. **`delayed-auto` vs `auto`**: recomendo delayed (boot mais saudável); se o requisito for "agent ativo o quanto antes", usar `auto`.
3. **Notificações toast nativas do Windows no serviço** (via `go-toast`/Win32 quando sem UI): adicionar ou manter apenas persist+headless?
4. **Campo `uiOnline` no heartbeat**: informar ao servidor se há UI ativa (útil para remote session). Incluir agora ou deixar para depois?
5. **Serviço lançar UI no logon via `CreateProcessAsUser`** como fallback à Scheduled Task (mais robusto, mais complexo): manter Scheduled Task existente é suficiente?

## 6. Ordem de execução sugerida

```
Fase 0 (busy_timeout + logs)          → ~meio dia
Fase 1 (modo serviço + CoreAgent)     → 2-3 dias (maior risco)
Fase 2 (IPC pipe)                     → 1-2 dias
Fase 3 (NSIS install/uninstall)       → ~meio dia
Fase 4 (self-update ciente)           → ~meio dia
Fase 5 (testes + checklist)           → 1 dia
```

Cada fase termina com `go test ./app/...` verde e build via `wails build` validado.

## 7. Fase futura — Remote session sem usuário logado (base: MeshAgent)

> Análise 2026-09-04 contra `Ylianst/MeshAgent` (kvm.c + ILibProcessPipe). Estado atual
> (pós-correção da sessão 4): remote session roda na **UI companion** — funciona com
> usuário logado, mas **não** cobre tela de logon (sem usuário) nem secure desktop (UAC).
>
> **IMPLEMENTADO 2026-09-04 (Fase 7)** — ver §7.4. Build/testes verdes. Pendente:
> validação em VM (logon screen, UAC, lock, multi-sessão RDP).

### 7.1 Como o MeshAgent resolve (referência)

```
Serviço SYSTEM (sessão 0)                    Processo filho KVM (sessão interativa)
  kvm_relay_setup / kvm_relay_feeddata          kvm_server_mainloop_ex
  ├─ rede ↔ stdin/stdout do filho (bridge)  ←→    ├─ CheckDesktopSwitch(): OpenInputDesktop
  ├─ gProcessSpawnType:                          │   + SetThreadDesktop (anexa ao desktop ativo)
  │   • SpawnTypes_USER → WTSQueryUserToken      ├─ GetUserObjectInformationA detecta troca
  │     + CreateProcessAsUser (sessão console)   │   de desktop (logon/UAC/lock) → refresh
  │   • SpawnTypes_WINLOGON → spawn em           ├─ Captura (funciona no secure desktop)
  │     winsta0\winlogon (tela de logon!)        └─ KeyAction/MouseAction (SendInput) —
  └─ kvm_relay_restart (relança se filho morre)      funciona pois o thread está anexado
```

Pontos-chave: (1) spawn em `winsta0\winlogon` quando não há usuário — captura a tela
de logon; (2) `CheckDesktopSwitch` no loop de captura resolve UAC/lock screen;
(3) serviço fica só com a rede e faz bridge stdin/stdout com o processo da sessão.

### 7.2 Design proposto para o Discovery

1. **Worker de remote session**: serviço recebe `remotesessionstart` →
   `WTSQueryUserToken` (sessão do console) + `CreateProcessAsUser` lançando
   `discovery-agent.exe --remote-session-worker <sessionId>` na sessão interativa.
   Sem usuário logado → spawn no `winsta0\winlogon` (token do winlogon — mais complexo,
   requer `WTSGetActiveConsoleSessionId` + token do processo winlogon).
2. **Comunicação serviço↔worker**: stdin/stdout framed (padrão MeshAgent) ou named
   pipe dedicado por sessão. O worker publica frames no NATS diretamente (reusar
   `ensureCompanionNats` como base).
3. **`CheckDesktopSwitch` no loop de captura** (`session_screen.go`):
   `OpenInputDesktop` + `SetThreadDesktop` a cada iteração; nome do desktop mudou →
   força refresh. Resolve UAC e lock screen também na UI companion atual.
4. **Fallback atual mantido**: UI companion presente → comando via IPC (implementado
   na sessão 4); worker só é necessário quando não há UI (logon screen) ou para
   secure desktop.

### 7.3 Esforço estimado

| Item                                               | Esforço                                  |
| -------------------------------------------------- | ---------------------------------------- |
| Spawn `CreateProcessAsUser` na sessão do console   | ~1 dia                                   |
| Spawn em winlogon (sem usuário)                    | ~2-3 dias (token winlogon, testes em VM) |
| Bridge stdin/stdout framed serviço↔worker          | ~1 dia                                   |
| `CheckDesktopSwitch` no loop de captura            | ~meio dia                                |
| Testes (VM sem login, UAC, lock, multi-sessão RDP) | ~1 dia                                   |

### 7.4 Implementação (2026-09-04) — build/testes verdes

| Item do §7.2                               | Arquivo(s)                                                                                                                                                                                                                                                                           | Status |
| ------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------ |
| 3. `CheckDesktopSwitch` no loop de captura | `core/screen/desktop_switch.go` (novo: `OpenInputDesktop`+`SetThreadDesktop`+`GetUserObjectInformationW`, `CurrentDesktopName`); `DirtyDetector.Reset()` (dirty_rects.go); integração no loop de captura (`session_screen.go` — troca de desktop → reset dirty detector + key frame) | ✅     |
| 1. Worker `--remote-session-worker`        | `remote_session_worker.go` (payload via stdin framed 4B+JSON, NATS dedicado, monitor de stdin EOF/"stop"); `remote_session_worker_config.go` (lê debug_config.json + config.json sem App); modo registrado no `main.go`                                                              | ✅     |
| 2. Bridge serviço↔worker                   | stdin framed (4B len + JSON) — payload nunca na linha de comando; stop via stdin `"stop"` ou EOF                                                                                                                                                                                     | ✅     |
| Spawn na sessão do console                 | `remote_session_worker_spawn.go`: `WTSGetActiveConsoleSessionId` + `WTSQueryUserToken` + `CreateProcessAsUser` (via `SysProcAttr.Token`), `CREATE_BREAKAWAY_FROM_JOB`                                                                                                                | ✅     |
| Spawn em winlogon (sem usuário)            | `tokenFromWinlogon`: enumera winlogon.exe da sessão do console (Toolhelp32 + ProcessIdToSessionId), `OpenProcessToken` + `DuplicateTokenEx(TokenPrimary)`                                                                                                                            | ✅     |
| 4. Integração no dispatch                  | `remote_debug_commands.go`: ServiceMode → UI conectada? IPC : (stop→worker, start→`spawnRemoteSessionWorker`); erro claro se sem sessão interativa                                                                                                                                   | ✅     |

**Fluxo final:**

```
remotesessionstart via NATS → serviço (sessão 0)
  ├─ UI companion conectada → IPC remote_session → UI executa (sessão usuário)
  └─ Sem UI → spawnRemoteSessionWorker:
       ├─ usuário logado → WTSQueryUserToken(console) + CreateProcessAsUser
       └─ sem usuário    → token do winlogon(console) + CreateProcessAsUser
                            (worker captura a tela de logon)
       worker: stdin(payload) → NATS dedicado → Manager.HandleCommand
       stop: stdin "stop" / EOF / expiração do manager
```

**Pendente (validação em VM):** tela de logon sem usuário, prompt UAC (secure
desktop via CheckDesktopSwitch), lock screen, sessão RDP múltipla, kill do
worker (relançamento pelo serviço em novo start).
