# Discovery Agent — Arquitetura e Índice de Código

> **Propósito deste documento:** Guia de referência rápida para desenvolvedores e agentes de IA. Descreve a estrutura real do projeto, responsabilidades de cada módulo e como os componentes se relacionam.

---

## 1. Visão Geral

O **Discovery Agent** é um agente de inventário, automação, acesso remoto e gerenciamento de TI para Windows. Ele combina:

- **Arquitetura Dual-Binary (`discovery-service.exe` + `discovery-agent.exe`)**:
  - Serviço Windows `SYSTEM` headless (`cmd/discovery-service` → `discovery-service.exe`), **sem Wails e sem WebView2**, responsável por toda a operação de core (NATS JetStream / `agentconn`, inventário, automação, P2P, self-update, remote session workers e servidor IPC).
  - UI Companion Desktop (`src/main.go` → `discovery-agent.exe`) via [Wails v3](https://wails.io/) (`v3.1.0` em [src/go.mod](src/go.mod)), rodando na sessão do usuário e comunicando-se com o serviço via **Dual-Pipe IPC** (named pipes separados de leitura e gravação/admin).
- **Acesso Remoto Completo**: tela remota (DXGI/GDI), terminal remoto (ConPTY com `RUN_AS`), transferência de arquivos binária (`fileserver`), administração de processos/serviços (`sysctrl`), editor/visualizador do Registro do Windows (`winregistry` com guard fail-closed), Proxy LAN HTTP/WS (`netproxy` com allowlist fail-closed e proteção SSRF) e túnel RDP multiplexado sobre NATS (`session_tunnel` + `--rdp-tunnel`).
- **Segurança Local Endurecida**: `authToken` protegido com DPAPI do Windows (`dpapi:v1:`), `config.json` e `debug_config.json` restritos a `SYSTEM`/`Administrators`, redação de segredos no IPC para a UI, `debug_http` autenticado por token e descomissionamento validado.
- **Ferramentas MCP internas** para integração com o chat de IA embarcado e **Rede P2P** (`go-libp2p` sem WebRTC/pion, mDNS/UDP) para distribuição local de artefatos.

> **Regra de Build:** a validação e compilação oficiais usam **Wails CLI v3** (`cd src && wails3 build` — ver [AGENTS.md](AGENTS.md)), nunca `go build` direto para o binário de UI, pois o frontend e os bindings JS são processados e embedados pelo Wails v3. A referência atual está alinhada com `v3.1.0` em [src/go.mod](src/go.mod).

### Modos de Execução e Binários

| Binário / Modo | Entrypoint / Trigger | Descrição |
| --- | --- | --- |
| `discovery-service.exe` | `cmd/discovery-service/main.go` | Serviço Windows `SYSTEM` (headless): `RunServiceMode` → `RunCore`, NATS/JetStream, inventário, automação, P2P, self-update, workers (`--remote-session-worker`, `--terminal-dispatcher`) e IPC named pipes (`--rdp-tunnel` para forwarding local). Zero dependências Wails/WebView2. |
| `discovery-agent.exe` | `main.go` (build tag `wailsui`) | UI Companion Wails v3 na sessão interativa com system tray, notificações nativas, chat IA, suporte e ponte IPC para o serviço. |
| MCP (interno) | Registry `core/mcp` | Tool calling interno do chat de IA do agent (`AppBridge`). |

### Componentes Principais

| Componente | Caminho | Papel |
| --- | --- | --- |
| Serviço Windows | `src/cmd/discovery-service` | Core do agente rodando como `SYSTEM` — **sem UI, sem Wails, sem WebView2** |
| UI Companion | `src/main.go` + `src/app/app_ui_wails.go` | Aplicação Wails v3 (`-tags wailsui`), janela principal preguiçosa, tray e overlay de captura |
| Core Hub (`package app`) | `src/app/app*.go` + `src/app/coreagent/` | `App` struct decomposto (`app.go`, `app_new.go`, `app_lifecycle.go`, `app_window_ui.go`, `app_connectivity.go`, `app_inventory_sync.go`, `app_power_idle.go`) + `CoreAgent` embed |
| Proxy LAN | `src/app/core/netproxy` | Proxy HTTP/WS para destinos da rede interna do endpoint, com allowlist fail-closed, proteção SSRF e resolução DNS anti-TOCTOU |
| Sessão Remota | `src/app/core/remotesession` | Orquestração de tela, terminal, arquivos, processos/serviços, túnel TCP/RDP multiplexado sobre NATS (`session_tunnel*`) e preflight RDP (`rdp_preflight*`) |
| Transporte WebRTC (opcional) | `src/app/core/remotesession/webrtc_*.go` | WebRTC ADITIVO para tela, túnel RDP/TCP e arquivos: política herdável (`ParseWebRtcPolicy`), decisão de transporte fail-closed (`resolveWebRtcTransport`), sinalização por subjects dedicados (`.webrtc.in`/`.webrtc.out`) e indicador de conexão publicado no `.control`. Desligado por padrão; a mídia pode ser P2P, mas o CONTROLE permanece no NATS |
| Captura / Terminal / Arquivos | `src/app/core/screen`, `core/terminal`, `core/fileserver`, `core/sysctrl`, `core/winregistry` | Captura DXGI/GDI, ConPTY (`RUN_AS`), servidor de arquivos com protocolo binário `'DF'`, gerenciamento de processos/serviços Windows com guard fail-closed, navegação/edição do Registro com guard fail-closed e codec de valor byte-exato |
| Identidade e Segurança | `src/app/core/platform` | DPAPI para `authToken`, ACLs endurecidas (`SYSTEM`/`Administrators`), elevação e verificação Authenticode |
| Testes E2E e Contrato | `src/e2e/` | Suíte Tiers 1–2 validando limites de arquitetura, desacoplamento `discovery-service`, `gofmt`/`go vet`, CLI flags e contratos de documentação |

---

## 2. Estrutura de Diretórios

```
Discovery/
├── .github/workflows/          # CI: pr-checks.yml e release-agent-on-tag.yml
├── build/scripts/              # Scripts PowerShell de build e empacotamento NSIS
├── src/                        # Módulo Go principal (go.mod: module discovery)
│   ├── main.go                 # Entry point UI (Wails v3, -tags wailsui)
│   ├── tray_embed.go           # //go:embed dos ícones do system tray
│   ├── startup_debug_keys_*.go # Detecta Shift/Ctrl no startup para DebugMode
│   ├── Taskfile.yml            # Tarefas Wails v3 (generate bindings, build, package)
│   ├── go.mod                  # Dependências Go (wails/v3 v3.1.0 + replace third_party/go-libp2p)
│   ├── cmd/
│   │   ├── discovery-service/  # Entry point headless do serviço Windows SYSTEM (0 deps Wails)
│   │   └── *probe / webview-sim/ # Ferramentas auxiliares de diagnóstico e simulação
│   ├── app/                    # Pacote principal (package app) + subpacotes de domínio
│   │   ├── app.go              # Constantes, GetDataDir, WebView2UserDataPath, App struct, Ctx
│   │   ├── app_new.go          # NewApp() e helpers de wiring (initCoreServices, initAutomationWiring, ...)
│   │   ├── app_lifecycle.go    # RunCore, ServiceShutdown, startup, runCoreStartup, runStagedStartup, shutdown
│   │   ├── app_window_ui.go    # SetApplication, SetMainWindow, Show/Hide/Minimise, QuitApp, EmitEvent
│   │   ├── app_connectivity.go # Heartbeat metrics, NATS connected/connectivity callbacks, SendTestHeartbeat
│   │   ├── app_inventory_sync.go # runPeriodicInventorySync, onPostBootstrapProvisioned, throttle, osquery check
│   │   ├── app_power_idle.go   # deferredRestartState, beginActivity, idle release memory
│   │   ├── app_ui_wails.go     # Adapters Wails v3 (//go:build wailsui)
│   │   ├── ui_contracts.go     # Interfaces puras Go (uiApp, uiWindow, uiSystemTray, uiNotifications)
│   │   ├── remote_services.go  # Adapters de serviços remotos Wails v3 (//go:build wailsui)
│   │   ├── ipc_*.go            # Dual-Pipe IPC (canal leitura + canal admin), auth e companion RPC
│   │   ├── coreagent/          # CoreAgent struct embutido em App e tipos desacoplados
│   │   ├── agentcommands/      # Handlers modulares de comandos do agente
│   │   ├── agentconfig/        # AgentConfiguration, defaults de PSADT/rollout e fetch
│   │   ├── apiclient/          # Cliente HTTP autenticado para a API RMM
│   │   ├── apimetrics/         # Telemetria de latência e erro por endpoint
│   │   ├── appstore/           # Tipos, cache e loader de políticas da App Store
│   │   ├── automation/         # Tipos de visão da automação para UI
│   │   ├── consolidation/      # Consolidação de eventos (realtime / 1min / 5min)
│   │   ├── customfields/       # Coleta e envio de custom fields
│   │   ├── debug/              # Config (DPAPI), bootstrap de credenciais, persistência e ACL
│   │   ├── debughttp/          # Servidor debug_http autenticado por token e broker SSE do chat
│   │   ├── decommission/       # Validação de caminho/assinatura e execução de descomissionamento
│   │   ├── installer/          # Descoberta e normalização de InstallerConfig
│   │   ├── inventory/          # Service de inventário, Windows Update cache e sync payloads
│   │   ├── logs/               # Ring buffer thread-safe, sanitização de tokens e persistência
│   │   ├── netutil/            # Headers X-Agent-ID / Bearer e sanitização de erros HTTP
│   │   ├── p2p/                # Coordinator P2P, chunk scheduler, gossip, mDNS/UDP/libp2p, onboarding
│   │   ├── p2pmeta/            # DTOs e cache leve do domínio P2P
│   │   ├── services/           # chat, hardwareid (TPM EK + SMBIOS), locale, memory, notifications, psadt
│   │   ├── status/             # Formatação de servidor/transporte e fallback de Overview
│   │   ├── support/            # Service de chamados e knowledge base
│   │   ├── supportchat/        # Chat de suporte em tempo real vinculado ao chamado
│   │   ├── supportmeta/        # DTOs e cache de suporte/chat
│   │   ├── sync/               # Coordenador de sincronização periódica e manifest
│   │   ├── syncmeta/           # Metadados de sincronização
│   │   ├── uiruntime/          # Probe nativo de responsividade da janela
│   │   ├── updates/            # Upgrades pendentes (winget) e exportador (Markdown/PDF)
│   │   └── core/               # Pacotes internos leaf (proibido importar discovery/app ou Wails)
│   │       ├── agentconn/      # Conexão NATS/WSS, JetStream, reconexão, heartbeat e dispatch
│   │       ├── ai/             # Cliente LLM OpenAI-compatible + tool calling MCP
│   │       ├── automation/     # Motor de automação, cron, executor e variáveis de ambiente seguras
│   │       ├── buildinfo/      # Version / CommitForReport via ldflags e VCS stamping
│   │       ├── chocolatey/     # Cliente Chocolatey
│   │       ├── consent/        # Políticas e modelo de consentimento remoto
│   │       ├── ctxutil/        # Helpers de contexto
│   │       ├── data/           # HTTPClient com cache ETag/SQLite
│   │       ├── database/       # Persistência SQLite (modernc.org/sqlite, WAL, outboxes, cache, memórias)
│   │       ├── dto/            # DTOs compartilhados do core
│   │       ├── envutil/        # Helpers de variáveis de ambiente
│   │       ├── errutil/        # Utilitários de erro
│   │       ├── export/         # Renderers Markdown/PDF e redação de PII
│   │       ├── fileserver/     # Servidor de arquivos remoto com handle cache e framing binário 'DF'
│   │       ├── inventory/      # Coletor nativo Windows + osquery Thrift + PowerShell fallback
│   │       ├── logger/         # Logging estruturado do core
│   │       ├── logstore/       # Banco unificado de logs (logs.db)
│   │       ├── maintenance/    # Cálculo de janelas de manutenção (IANA)
│   │       ├── mcp/            # Registry MCP e AppBridge
│   │       ├── memlimit/       # Relaxamento controlado de memória do runtime Go
│   │       ├── models/         # Modelos compartilhados (InventoryReport, UpgradeItem, etc.)
│   │       ├── netproxy/       # Proxy LAN HTTP/WS + allowlist SSRF-safe + anti-TOCTOU
│   │       ├── nettable/       # Tabela de conexões de rede
│   │       ├── notifyui/       # Contratos de notificação da UI
│   │       ├── platform/       # DPAPI, ACLs SYSTEM/Admins, paths ProgramData, Authenticode
│   │       ├── printer/        # Gerenciamento de impressoras
│   │       ├── processutil/    # Execução de processos, Job Objects, eficiência, tokens de sessão
│   │       ├── remotedebug/    # Sessões de debug remoto sobre NATS
│   │       ├── remotesession/  # Gerenciador de acesso remoto, workers, tunnel TCP/RDP e preflight
│   │       ├── runtimediag/    # Diagnóstico periódico de heap/GC/goroutines
│   │       ├── safego/         # Lançamento de goroutines com recovery de panic e stack trace
│   │       ├── screen/         # Captura de tela DXGI Desktop Duplication / GDI
│   │       ├── screenshot/     # Captura assistida do chat IA, política e rate limiter
│   │       ├── selfupdate/     # Auto-atualização via P2P/HTTP com verificação SHA-256
│   │       ├── services/       # Wrappers de domínio (CatalogService, AppsService, InventoryService, PrinterService)
│   │       ├── sessioncontrol/ # Codec único e allowlist fechada de frames de controle (512B max)
│   │       ├── sessionlive/    # Estado de sessão remota ativa
│   │       ├── sysctrl/        # Processos/serviços Windows com guard fail-closed para DCs e serviços críticos
│   │       ├── winregistry/    # Registro do Windows: navegação/edição com guard fail-closed e codec byte-exato
│   │       ├── terminal/       # Pseudo-console ConPTY e controle de execução SYSTEM vs usuário
│   │       ├── thirdparty/     # Provedores de patches de terceiros (choco/winget)
│   │       ├── tlsutil/        # Cliente HTTP e pinagem/validação TLS
│   │       ├── windowsupdate/  # Cliente WUA (COM) para inventário de patches Windows
│   │       ├── winget/         # Cliente Winget
│   │       └── winui/          # Helpers Win32 nativos
│   ├── frontend/               # Frontend Web (HTML + vanilla JS + módulos ES)
│   │   ├── index.html          # Shell principal da aplicação
│   │   ├── app.js              # Bootstrapper da interface
│   │   ├── wails-bridge.js     # Bridge Wails v3: importa ./bindings/discovery/app/app.js e /wails/runtime.js
│   │   ├── bindings/           # Bindings JS gerados por `wails3 generate bindings` (não editar)
│   │   │   └── discovery/app/app.js
│   │   ├── js/                 # Módulos JavaScript por funcionalidade
│   │   ├── partials/           # Fragmentos HTML (shell, sidebar, views)
│   │   └── styles/             # Folhas de estilo modulares
│   ├── e2e/                    # Testes E2E Tiers 1–2 (boundaries, F14 decoupling, F18 CLI/go.mod)
│   ├── build/                  # Configuração Wails v3 (config.yml, windows/project.nsi, Taskfile.yml)
│   └── third_party/go-libp2p/  # Fork local de go-libp2p sem transporte WebRTC (pion/webrtc removido)
├── AGENTS.md                   # Regras obrigatórias de build Wails v3
├── BRANCHING.md                # Fluxo de branches (dev → beta → release → lts)
├── SECURITY.md                 # Política de segurança
├── README.md                   # Visão geral e guia rápido
└── ARCHITECTURE.md             # Este arquivo
```

---

## 3. Diagrama de Dependências

```
cmd/discovery-service/main.go (SYSTEM, sem Wails/WebView2)
   │
   └──► package app (RunCore → startup → runCoreStartup → runStagedStartup)
           ├──► app/coreagent        (CoreAgent embed e tipos desacoplados)
           ├──► app/debug, inventory, support, updates, p2p, sync, automation, ...
           ├──► Dual-Pipe IPC        (IPCServer: pipe leitura + pipe admin)
           └──► app/core/*           (leaf packages: zero imports de discovery/app ou Wails)
                   ├── agentconn, automation, database, inventory, selfupdate
                   ├── netproxy, remotesession, fileserver, screen, terminal, sysctrl, winregistry
                   ├── sessioncontrol, platform (DPAPI/ACL), safego, tlsutil
                   └── ...

main.go (-tags wailsui, sessão do usuário)
   │
   ├──► package app (app_ui_wails.go, remote_services.go, app_window_ui.go)
   │       ├──► Wails v3 Application (`github.com/wailsapp/wails/v3 v3.1.0`)
   │       ├──► frontend/wails-bridge.js → bindings/discovery/app/app.js + /wails/runtime.js
   │       └──► IPCClient (conecta aos named pipes do discovery-service.exe)
   └──► startup_debug_keys_*.go
```

> **Invariante de Desacoplamento (`TestF14_T1_*`):**
> - `./cmd/discovery-service` possui **0 dependências transitivas** de `github.com/wailsapp/wails/*`, `webview2` ou `pion/webrtc`.
> - Nenhum pacote em `./app/core/...` importa `discovery/app` ou `github.com/wailsapp/wails/*`.

---

## 4. Índice de Arquivos Chave

### Raiz de `src/`

| Arquivo | Responsabilidade |
| --- | --- |
| `main.go` | Entry point da UI Companion Wails v3 (`-tags wailsui`). Configura single-instance, tray, janela principal preguiçosa e serviços Wails (`App`, `RemoteDebugService`, `RemoteSessionService`). |
| `cmd/discovery-service/main.go` | Entry point headless do serviço Windows `SYSTEM` e modo worker (`--remote-session-worker`, `--terminal-dispatcher`, `--rdp-tunnel`). |
| `tray_embed.go` | `//go:embed` dos ícones do system tray passados via `AppStartupOptions`. |
| `startup_debug_keys_*.go` | Detecta Shift/Ctrl na inicialização do Windows para ativar `DebugMode`. |

### Decomposição do Hub `src/app/` (`app*.go`)

| Arquivo | Responsabilidade |
| --- | --- |
| `app.go` | `Version`, constantes de janela/timeouts, `GetDataDir()`, `WebView2UserDataPath()`, definição da `App` struct (com `coreagent.CoreAgent` embutido), `GetRuntimeFlags()`, `SetContext()`, `Ctx()`, `GetServiceHealth()`. |
| `app_new.go` | Construtor `NewApp(opts)` decomposto em helpers coesos (`initCoreServices`, `initAutomationWiring`, `initConnectivityAndRemote`, `initInventoryWiring`, `initSelfUpdater`, `initSupportAndDebug`). |
| `app_lifecycle.go` | `RunCore()`, `ServiceShutdown()`, `startup()`, `runCoreStartup()`, `runStagedStartup()`, `serviceAgentConnDelay()`, `shutdown()`, `GetStartupError()`, `startupLogf()`, `safeGo()`. |
| `app_window_ui.go` | `SetApplication()`, `SetMainWindow()`, `ShowMainWindow()`, `HideMainWindow()`, `MinimiseMainWindow()`, `QuitApp()`, `EmitEvent()`, `normalizeEventArgs()`, `RequestAppClose()`, `ShouldHideOnClose()`, `IsTrayReady()`, `hideWindowOnStartup()`, `ClearMemoryCaches()`. |
| `app_connectivity.go` | `getHeartbeatMetrics()`, `mergeHeartbeatMetrics()`, `getKnownP2PPeers()`, `onNatsConnected()`, `onConnectivityChange()`, `SendTestHeartbeat()`, `GetAgentConfiguration()`, `featureEnabled()`, estado zero-touch. |
| `app_inventory_sync.go` | `runPeriodicInventorySync()`, `isInventoryProvisioned()`, `applyStartupThrottleConfig()`, `onPostBootstrapProvisioned()`, `ensureOsqueryInstalled()`. |
| `app_power_idle.go` | `deferredRestartState`, `cancelDeferredRestart()`, `scheduleDeferredRestart()`, `beginActivity()`, `applyIdleMode()`, `scheduleIdleRelease()`, `cancelIdleRelease()`, `releaseIdleMemory()`. |

---

## 5. Fluxos Principais

### Startup Dual-Binary (Serviço + UI Companion)

```
1. discovery-service.exe (Sessão 0 / SYSTEM)
   main() → RunServiceMode()
     └─► NewApp(ServiceMode: true)
     └─► RunCore(ctx) → startup(ctx)
           ├─► StartIPCServer()                // Dual-Pipe IPC (leitura + admin)
           ├─► runCoreStartup(ctx)
           │     ├─► RemoteDebug.Startup()     // lifecycle no serviço
           │     ├─► database.Open()           // SQLite WAL + manutenção periódica
           │     ├─► resumeAgentDecommissionIfPending()
           │     └─► runStagedStartup(ctx)
           │           ├─► Phase 1 (2s): inventário inicial
           │           ├─► Phase 2 (1s svc): bootstrap DPAPI + AgentConn.Run (NATS/WSS)
           │           ├─► Phase 3 (10s): AutomationSvc, AutoUpdateSvc, SyncSvc, P2PCoord, APIHealthProbe
           │           └─► Phase 4 (12s): SelfUpdater, outbox/inventory cleanup + WUA probes
           └─► startUIWatchdog(ctx)            // relança UI companion na sessão interativa

2. discovery-agent.exe (Sessão do usuário, -tags wailsui)
   main()
     └─► NewApp(ServiceMode: false)
     └─► application.New(...)                  // Wails v3 runtime + system tray + lazy mainWindow
     └─► ServiceStartup(ctx) → RunCore(ctx) → startup(ctx)
           ├─► startIPCClient()                // conecta ao serviço via named pipes (sem DB local)
           ├─► EnsureChatSSEServer()           // SSE autenticado para chat nativo
           ├─► startTray()                     // systray + menu dinâmico
           └─► applyIdleMode(true)             // modo econômico quando minimizado/ocioso
```

### Acesso Remoto, Proxy LAN e Túnel RDP

```
Console Web (DiscoveryRMM_Site) ──► API WS/NATS ──► discovery-service.exe (agentconn)
  ├─► Tela / Terminal / Arquivos / Processos:
  │     └─► remotesession.Manager → spawn worker (--remote-session-worker / --terminal-dispatcher)
  │           ├─► screen (DXGI-first + GDI fallback)
  │           ├─► terminal (ConPTY, RUN_AS fail-closed para usuário sem fallback para SYSTEM)
  │           ├─► fileserver (handle cache + protocolo binário 'DF')
  │           ├─► sysctrl (guard fail-closed para serviços críticos / DC)
│           └─► winregistry (guard fail-closed; Run/RunOnce = leitura apenas)
  ├─► Registro do Windows:
  │     └─► SessionRegistry (.registry.req / .registry.resp / .registry.ready)
  │           ├─► winregistry.guard (FAIL-CLOSED: SAM/SECURITY/LSA nunca; Run/RunOnce só leitura)
  │           └─► winregistry.ops (ListKey paginado, Get/Set/Delete, Create/Delete/Rename, Export)
  └─► Proxy LAN & Túnel RDP:
        ├─► netproxy: HTTP/WS local allowlist SSRF-safe + anti-TOCTOU DNS
        ├─► session_tunnel (.tunnel.in / .tunnel.out): multiplexação TCP sobre NATS + --rdp-tunnel
        └─► WebRTC opcional (webrtc.in / webrtc.out): sinalização SDP/ICE fora do
            .control (cujo teto de 512 B não comporta um SDP). Só é negociado
            quando a política herdável está efetiva; a falha de ICE cai para NATS
            e o estado vai ao indicador (nats | webrtc | webrtc-fallback).
```

### P2P Artifact Distribution & Onboarding

```
p2p.Coordinator.Run()
  ├─► discoveryTick()             // mDNS / UDP / go-libp2p DHT (sem WebRTC/pion)
  ├─► TransferServer              // HTTP local P2P
  ├─► replicationWorkers          // push validado entre peers
  └─► DownloadArtifactByIDSwarm() // download paralelo em chunks + verificação SHA-256
```

---

## 6. Padrões de Código e Convenções

- **Métodos expostos ao frontend**: receptor `*App` em `package app`, exportados e compatíveis com `wails3 generate bindings` (`window.go.app.App.*`). Métodos internos em `*App` que não devem virar bindings levam `//wails:ignore`.
- **Separação Core vs UI**: `src/app/core/*` **nunca** importa `discovery/app` nem `github.com/wailsapp/wails/v3`. Código dependente do Wails v3 fica isolado atrás da build tag `//go:build wailsui` (`app_ui_wails.go`, `remote_services.go`).
- **Goroutines**: usar `a.safeGo(...)` (`core/safego.Go`) para captura de panics e stack trace.
- **Segurança de segredos e configuração**: `authToken`/`deployToken` protegidos com Windows DPAPI (`platform.ProtectSecret`), arquivos restritos a `SYSTEM`/`Administrators`, e DTOs redigidos via `redactDebugConfig` no IPC para a UI.

---

## 7. Build, Testes e Desenvolvimento

### Comandos Oficiais (Wails v3)

```powershell
# A partir de Discovery/src:
cd src

# Build completo de produção (discovery-agent.exe + discovery-service.exe em src/bin/)
wails3 build

# Desenvolvimento com hot-reload
wails3 dev -config ./build/config.yml

# Gerar bindings JS v3 manualmente (frontend/bindings/discovery/app/app.js)
wails3 generate bindings -b -clean=true -f "-tags wailsui" -d frontend/bindings ./...
```

> **Importante:** Nunca use `go build` diretamente para compilar o binário GUI `discovery-agent.exe` — ele não processa assets nem bindings do frontend. Consulte [AGENTS.md](AGENTS.md).

### Verificação Estática e Testes Go

```powershell
cd src
$env:GOWORK = "off"
$env:CGO_ENABLED = "1"

gofmt -l ./app ./cmd ./e2e
go vet ./app/... ./cmd/discovery-service/... ./e2e/...
go vet -tags wailsui .
go test -race ./app/... -timeout 600s
go test -v ./e2e -run "TestF14_|TestF18_"
```

### Wails v3 JS Bindings

Os arquivos em `src/frontend/bindings/` (incluindo `src/frontend/bindings/discovery/app/app.js`) são **gerados automaticamente** pelo `wails3 generate bindings` / `wails3 build`. O módulo [src/frontend/wails-bridge.js](src/frontend/wails-bridge.js) importa os bindings v3 e `/wails/runtime.js`, expondo `window.go.app.App.*` e `window.wails` para os scripts da UI.

---

## 8. Dependências Notáveis

| Dependência | Uso |
| --- | --- |
| `github.com/wailsapp/wails/v3` (`v3.1.0`) | Framework GUI desktop para `discovery-agent.exe` (bindings, runtime, janelas, tray nativo) |
| `github.com/libp2p/go-libp2p` (fork `./third_party/go-libp2p`) | Stack P2P (DHT, mDNS, TCP/QUIC; transporte `pion/webrtc` removido) |
| `github.com/nats-io/nats.go` | Comunicação com servidor central via NATS / WebSocket e JetStream |
| `modernc.org/sqlite` | Driver SQLite pure Go (cache local, outboxes duráveis, logs) |
| `github.com/robfig/cron/v3` | Agendamento cron para automação |
| `github.com/kirides/go-d3d` | Captura de tela de baixa latência via DXGI Desktop Duplication |
| `github.com/google/go-tpm` | Leitura da Endorsement Key (EK) do TPM 2.0 para identidade de hardware |
| `github.com/osquery/osquery-go` | Cliente Thrift opcional para coleta via socket `osquery` |
| `github.com/go-pdf/fpdf` | Geração de relatórios em PDF |
| `github.com/gorilla/websocket` | Suporte WebSocket para proxy LAN / túneis |
| `github.com/Microsoft/go-winio` | Named pipes Windows com DACLs explícitas para o Dual-Pipe IPC |
