# Plano: Agent como Serviço Windows (SYSTEM) + UI Companion no Usuário

> Status: **PROPOSTA — aguardando revisão antes da implementação**
> Data: 2026-08-30
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

1. **SQLite multi-processo**: adicionar `PRAGMA busy_timeout=5000` em `database.Open()` (`src/app/core/database/sqlite.go`). Hoje serviço e UI vão abrir o mesmo `discovery.db` em `C:\ProgramData\Discovery` — sem busy_timeout, `SQLITE_BUSY` vira erro intermitente.
2. **Log do serviço**: garantir que `logger` escreve em `%ProgramData%\Discovery\logs\agent-service.log` quando em modo serviço (hoje `LogFilePath()` já aponta para `agent.log`; separar arquivo por modo facilita diagnóstico).
3. Confirmar que `EmitEvent`/`DispatchNotification` já toleram ausência de UI (verificado: `headless_no_context` já existe em `services/notifications/service.go` — apenas garantir que o serviço injeta `ctx` não-nil mas sem `EmitEvent` real, caindo no caminho headless).

### Fase 1 — Modo serviço no binário (`--service`)

**Arquivos novos:**

- `src/app/servicemode/service_windows.go` — implementação `svc.Handler`:
  ```go
  type discoveryService struct{ app *app.CoreAgent }
  func (s *discoveryService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32)
  ```

  - Aceita `Start`, `Stop`, `Shutdown`, `Interrogate` (preenchendo `svc.Accepted` com `svc.AcceptStop|svc.AcceptShutdown`).
  - No `Start`: monta o **core do agent** (sem Wails) e roda o mesmo pipeline de `startup()` — porém **sem** tray, sem janela, sem debug HTTP de UI.
- `src/app/servicemode/service_other.go` — stub não-Windows.
- `src/app/servicemode/ipc_windows.go` — servidor named pipe `\\.\pipe\discovery-agent-ipc` (DACL permitindo Users + SYSTEM; padrão já usado no projeto em `core/terminal/dispatcher_windows.go`).

**Refactor necessário (o ponto mais delicado do plano):**

- `App.startup()` (`src/app/app.go:1027`) hoje mistura core + UI. Extrair para `CoreAgent` reutilizável:
  - `agentConn.Run`, `inventorySvc`, `automationSvc`, `syncSvc`, `p2pCoord`, `selfUpdater`, outboxes, consolidation engine, DB — movidos para um struct `CoreAgent` com método `Run(ctx)`.
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
- **Update (`/UPDATE`)**: `PrepareForInPlaceUpdate` precisa também parar o serviço (`sc stop DiscoveryAgent` + aguardar) antes do rename `.bak_update`; ao final, `sc start DiscoveryAgent` em vez de (ou além de) `Exec` da UI.
- Mantém a Scheduled Task `DiscoveryAgentUI` existente (agora ela só sobe a UI companion).

### Fase 4 — Self-update ciente do serviço

- `selfupdate/launch_windows.go`: quando em modo serviço (SYSTEM), o instalador `/S /UPDATE` já roda elevado — sem prompt UAC. Ajustar:
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

## 4. Riscos e Mitigações

| Risco                                                  | Impacto  | Mitigação                                                                                                                          |
| ------------------------------------------------------ | -------- | ---------------------------------------------------------------------------------------------------------------------------------- |
| Refactor de `App.startup()` quebrar UI                 | Alto     | Extração incremental com testes `go test ./app/...` após cada passo; manter `App` como wrapper de `CoreAgent`                      |
| Comandos duplicados (UI standalone + serviço)          | Médio    | Handshake IPC define dono único do core; UI só assume core se serviço ausente por N segundos                                       |
| `SQLITE_BUSY` entre processos                          | Médio    | `busy_timeout=5000` + WAL (já ativo)                                                                                               |
| Notificações `require_confirmation` sem usuário logado | Médio    | Caminho headless já existe (`timeout_policy_applied`); documentar comportamento                                                    |
| Remote session/terminal não funcionam sem login        | Esperado | Por design (exige sessão interativa); opcionalmente serviço pode lançar UI via `CreateProcessAsUser` (fase futura, fora do escopo) |
| `mgr.Connect` com SC_MANAGER_ALL_ACCESS no serviço     | Nenhum   | Serviço roda como SYSTEM — acesso garantido                                                                                        |
| Manifest `requireAdministrator` vs SCM                 | Nenhum   | SCM ignora `requestedExecutionLevel` ao lançar serviços                                                                            |
| SingleInstance Wails `com.discovery.app`               | Nenhum   | Serviço nunca cria `application.New`                                                                                               |

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
