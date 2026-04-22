# Discovery Agent — Arquitetura e Índice de Código

> **Propósito deste documento:** Guia de referência rápida para desenvolvedores e agentes de IA. Descreve a estrutura do projeto, responsabilidades de cada arquivo e como os componentes se relacionam.

---

## 1. Visão Geral

O **Discovery Agent** é um aplicativo de inventário e gerenciamento de TI para Windows. Ele combina:

- **UI Desktop** via [Wails v2](https://wails.io/) (frontend HTML/JS + backend Go)
- **Windows Service** headless (sem UI) para execução em background
- **Ferramentas MCP internas** para integração com o chat de IA embarcado no próprio agent
- **Rede P2P** para distribuição de artefatos (instaladores, políticas) entre agentes na mesma rede local
- **Automação** de tarefas via políticas baixadas do servidor central

### Modos de Execução

| Modo | Trigger | Descrição |
|------|---------|-----------|
| GUI (Wails) | padrão | Janela desktop com system tray |
| Windows Service | `--service` | Headless, sem UI, roda como serviço |
| MCP (interno) | n/a | Tool calling interno do chat de IA do agent |

---

## 2. Estrutura de Diretórios

> **Nota sobre o diretório `app/`:** a modularização está em andamento. Os domínios **debug**, **inventory**, **support** e **updates** já foram extraídos para subpacotes Go reais (`app/debug`, `app/inventory`, `app/support`, `app/updates`). O `package app` principal retém o hub central (`App` struct), os arquivos de bridge/delegate e os domínios P2P e automação que ainda possuem acoplamento direto ao estado central. A extração continua incrementalmente, preservando a API pública via bridges compatíveis.

```
discovery/
├── app/                        # Pacote principal da aplicação (package app)
│   ├── appstore/               # Subpacote real: tipos e cache da app store
│   │   └── types.go            # Item, Response, EffectivePolicy, PolicyCache
│   ├── automation/             # Subpacote real: tipos da automação para UI
│   │   └── types.go            # StateView, TaskView, ExecutionView
│   ├── debug/                  # Subpacote real: domínio de conexão/debug extraído
│   │   ├── config.go           # Config (DebugConfig): credenciais, TLS, NATS, flags
│   │   ├── service.go          # Service: bootstrap de token/agentID, persistência, conectividade
│   │   └── installer.go        # installerConfigPathCandidates + parser InstallerConfig
│   ├── inventory/              # Subpacote real: domínio de inventário extraído
│   │   ├── service.go          # Service: coleta, cache, installs, osquery, notificações
│   │   └── sync.go             # Payloads de inventário para o servidor (agentHardwareEnvelope etc.)
│   ├── netutil/                # Subpacote real: utilitários HTTP compartilhados
│   │   └── headers.go          # SetAgentAuthHeaders — headers de autenticação usados pelas APIs
│   ├── p2pmeta/                # Subpacote real: tipos e contratos leves do domínio P2P
│   │   └── types.go            # Config, views, telemetry, onboarding, seed-plan e cache leve
│   ├── support/                # Subpacote real: domínio de suporte/tickets extraído
│   │   ├── service.go          # Service: criação de tickets, prioridade, serialização para API
│   │   └── knowledge.go        # Knowledge base: fetch e cache de artigos de suporte
│   ├── supportmeta/            # Subpacote real: DTOs e cache leve de chat/suporte/tickets
│   │   └── types.go            # AgentInfo, workflow/tickets, knowledge base e cache leve
│   ├── updates/                # Subpacote real: domínio de atualizações extraído
│   │   ├── service.go          # Service: GetPendingUpdates, integração winget
│   │   └── export.go           # Exporter: exportação de relatório de updates (Markdown/PDF)
│   ├── Core
│   │   ├── app.go              # App struct — hub central, lifecycle, NewApp()
│   │   ├── types.go            # Tipos compartilhados e aliases compatíveis do pacote app
│   │   ├── bridge.go           # AppBridge — expõe métodos App ao MCP registry
│   │   ├── logging.go          # logBuffer — ring buffer em memória + arquivo
│   │   └── memory.go           # CRUD de notas locais
│   ├── Bridges (delegates para subpacotes)
│   │   ├── debug_bridge.go     # GetDebugConfig/SetDebugConfig — delega a debug.Service
│   │   ├── inventory_bridge.go # GetCatalog — delega a inventory.Service
│   │   ├── inventory_export.go # getInventoryForExport — helper de export de inventário
│   │   ├── logs_bridge.go      # GetLogs/ClearLogs — delega ao logBuffer
│   │   ├── p2p_bridge.go       # applyP2PConfig — aplicação de config P2P ao coordinator
│   │   ├── support_bridge.go   # GetAgentInfo e demais métodos — delega a support.Service
│   │   ├── updates_bridge.go   # GetPendingUpdates — delega a updates.Service
│   │   └── updates_helpers.go  # parseUpgradeOutput — wrapper sobre updates.ParseUpgradeOutput
│   ├── Configuração e Conexão
│   │   ├── agent_config.go     # AgentConfiguration: struct e loader (parse do JSON do servidor)
│   │   ├── agent_config_helpers.go # Helpers de acesso a AgentConfiguration (GetAgentConfiguration etc.)
│   │   ├── installer.go        # installerConfigPathCandidates — caminhos candidatos do config
│   │   └── sync.go             # syncCoordinator — sync de políticas/resources do servidor
│   ├── Inventário e App Store
│   │   ├── store.go            # App store catalog + cache (ETag + SQLite)
│   │   ├── printers.go         # Gerenciamento de impressoras
│   │   └── export_redact.go    # getRedact() — flag de redação de PII para exports
│   ├── Automação
│   │   ├── automation.go       # Bridge entre internal/automation.Service e frontend
│   │   └── automation_p2p.go   # automationPackageManagerRouter: P2P vs HTTP vs winget
│   ├── Notificações e Comandos
│   │   ├── notification_center.go   # NotificationDispatchRequest, DispatchNotification, loop de resposta
│   │   ├── command_result_outbox.go # Outbox pattern para resultados de comandos (SHA-256, retry)
│   │   └── consolidation_engine.go  # ConsolidationEngine: janela realtime/1min/5min para eventos
│   ├── Debug e Diagnóstico
│   │   ├── remote_debug.go     # remoteDebugManager: debug remoto com streaming de logs
│   │   └── psadt_debug_bridge.go # Bootstrap e diagnóstico do PSADT
│   ├── P2P
│   │   ├── headless_p2p_runtime.go # HeadlessP2PService: reutiliza o stack P2P do app no modo Windows Service
│   │   ├── p2p.go              # p2pCoordinator — núcleo: discovery, lifecycle, estado
│   │   ├── p2p_api.go          # Seed plan + telemetry (chamadas ao servidor central)
│   │   ├── p2p_chunks.go       # p2pChunkScheduler: swarm/chunks, bandwidth cap
│   │   ├── p2p_cleanup.go      # Limpeza de artefatos expirados e arquivos temporários
│   │   ├── p2p_cloud_bootstrap.go # Bootstrap via servidor externo (cloud seed)
│   │   ├── p2p_config.go       # Helpers de configuração P2P (normalização, defaults)
│   │   ├── p2p_discovery.go    # p2pDiscoveryProvider interface + mDNS e UDP broadcast
│   │   ├── p2p_download.go     # Download de artefatos (single peer e helpers)
│   │   ├── p2p_gossip.go       # Gossip pull: sincronização do índice de peers via libp2p
│   │   ├── p2p_http.go         # p2pTransferServer: servidor HTTP local P2P
│   │   ├── p2p_libp2p.go       # p2pLibP2PProvider + p2pMultiProvider (DHT, bootstrap)
│   │   ├── p2p_libp2p_transport.go # Transport layer libp2p (stream handling)
│   │   ├── p2p_onboarding.go   # p2pOnboardingState: loop de onboarding com backoff
│   │   ├── p2p_peer_cache.go   # Cache persistente de peers conhecidos (até 128 entradas)
│   │   ├── p2p_publish.go      # ListArtifacts e publicação de artefatos
│   │   ├── p2p_replication.go  # Workers de replicação: push de artefatos entre peers
│   │   ├── p2p_status.go       # Views de status P2P para UI
│   │   └── p2p_telemetry_outbox.go # Outbox de telemetria P2P (batch envio ao servidor)
│   └── UI e Suporte
│       ├── chat.go             # Chat AI: config, histórico, streaming, integração MCP
│       ├── mesh.go             # Instalação automática do MeshCentral Agent
│       ├── status.go           # GetStatusOverview — snapshot de saúde para UI
│       ├── tray.go             # System tray: ícone, menu, heartbeat watchdog
│       ├── ui_runtime.go       # Heartbeat/suspensão do runtime da UI para o watchdog
│       ├── ui_runtime_windows.go # Probe Win32 (EnumWindows/IsHungAppWindow) para validar a janela principal
│       ├── ui_runtime_other.go # Fallback cross-platform sem probe nativo
│       ├── tray_text_other.go  # Wrappers systray non-Darwin
│       └── tray_text_darwin.go # Wrappers systray Darwin
│
├── internal/                   # Pacotes internos (lógica sem dependência do App)
│   ├── agentconn/              # Conexão NATS/WebSocket com servidor central
│   │   └── runtime.go          # Runtime de conexão: config, reconexão, callbacks de ping e comandos
│   ├── ai/                     # Serviço de chat AI (LLM OpenAI-compatible)
│   │   ├── chat.go             # Service: SendMessage, StreamMessage, tool calling
│   │   └── formatting_test.go  # Testes de formatação de mensagens
│   ├── automation/             # Motor de automação (políticas, scripts, cron)
│   │   ├── service.go          # Service: sync de políticas, execução, callbacks
│   │   ├── executor.go         # Executa scripts PowerShell/Batch com timeout
│   │   ├── client.go           # HTTP client para API de automação
│   │   └── types.go            # Tipos: Task, Script, ExecutionRecord, State, PSADTPolicy
│   ├── buildinfo/              # Informações de build
│   │   └── version.go          # Version: variável de versão para uso em módulos internos
│   ├── ctxutil/                # Utilitários de contexto
│   │   └── timeout.go          # Timeout helpers
│   ├── data/                   # HTTP client com cache ETag + SQLite
│   │   └── http_client.go      # HTTPClient: GET com cache, retry, timeout
│   ├── database/               # Persistência SQLite
│   │   └── sqlite.go           # DB: CacheGet/SetJSON (KV+TTL), MemoryNotes CRUD
│   ├── export/                 # Exportação de inventário
│   │   ├── markdown.go         # Renderer Markdown via templates
│   │   ├── pdf.go              # Renderer PDF via fpdf
│   │   └── redaction.go        # Redação de PII (senhas, tokens, MACs, seriais)
│   ├── inventory/              # Coleta de inventário do sistema
│   │   ├── provider.go         # Orquestra osquery (preferred) + PowerShell (fallback)
│   │   ├── osquery.go          # Subprocess-based query runner (fallback) + cache do binário
│   │   ├── osquery_client.go   # osquery-go Thrift client: socket osqueryd / osqueryi socket mode
│   │   ├── powershell.go       # Queries WMI/CIM via PowerShell (fallback Windows)
│   │   ├── helpers.go          # Utilitários internos de parsing
│   │   └── mappers.go          # Mapeia raw query results → models.InventoryReport
│   ├── mcp/                    # MCP (Model Context Protocol) server e registry
│   │   ├── server.go           # JSON-RPC 2.0 server (stdio)
│   │   ├── tools.go            # Registry, Tool, Handler abstractions
│   │   ├── register.go         # RegisterDiscoveryTools + interface AppBridge
│   │   └── ping.go             # Ferramenta MCP de ping/diagnóstico
│   ├── models/                 # Tipos de dados compartilhados
│   │   └── (inventory.go, etc) # InventoryReport, HardwareInfo, SoftwareItem, UpgradeItem, etc.
│   ├── printer/                # Gerenciamento de impressoras
│   │   └── manager.go          # List, install, remove impressoras
│   ├── processutil/            # Utilitários de processo
│   │   └── (util.go)           # Run command, capture output, etc.
│   ├── selfupdate/             # Auto-atualização do agente
│   │   └── updater.go          # Updater: download + verificação SHA-256 + substituição binária
│   ├── service/                # Windows Service (modo headless)
│   │   ├── client.go           # ServiceClient: cliente da Named Pipe usado pela UI
│   │   ├── ipc.go              # IPCServer: protocolo e dispatch via Named Pipe
│   │   ├── manager.go          # ServiceManager: ciclo de vida do service e workers headless
│   │   └── runtime_services.go # Adapters de automação/inventário para o modo service
│   ├── services/               # Serviços de alto nível (wrappers)
│   │   ├── apps_service.go     # AppsService: install/uninstall/upgrade via winget
│   │   ├── catalog_service.go  # CatalogService: fetch + cache do app store
│   │   ├── inventory_service.go# InventoryService: coleta com timeout e progress
│   │   └── printer_service.go  # PrinterService: wrapper do printer.Manager
│   ├── watchdog/               # Health monitoring e recovery de goroutines
│   │   └── watchdog.go         # Watchdog: heartbeat, timeout, panic recovery
│   └── winget/                 # Cliente winget (Windows Package Manager)
│       └── client.go           # Run winget commands, parse tabular output
│
├── frontend/                   # Frontend Web (HTML + vanilla JS)
│   ├── index.html              # Shell principal da aplicação
│   ├── app.js                  # Bootstrapper + catalog/packages view
│   ├── styles.css              # Estilos globais
│   ├── js/                     # Módulos JavaScript por funcionalidade
│   │   ├── app-init.js         # Event bindings iniciais
│   │   ├── app-status.js       # Painel de status de conexão
│   │   ├── app-inventory.js    # Visualização de inventário
│   │   ├── app-p2p.js          # Painel P2P
│   │   ├── app-chat.js         # Interface de chat AI
│   │   ├── app-automation.js   # Painel de automação
│   │   ├── app-store-updates.js# Updates pendentes
│   │   ├── app-support.js      # Tickets de suporte
│   │   ├── app-knowledge.js    # Knowledge base
│   │   ├── app-debug.js        # Painel de debug de conexão
│   │   ├── app-utils.js        # Utilitários compartilhados
│   │   └── app-window.js       # Chrome da janela (min/max/close)
│   ├── partials/               # Fragmentos HTML (shell, sidebar, views)
│   └── wailsjs/                # Bindings JS gerados pelo Wails (não editar)
│       ├── go/app/App.js       # Métodos Go expostos ao JS (gerado; namespace window.go.app.App)
│       └── runtime/            # Runtime Wails (window, events, etc.)
│
├── build/                      # Artefatos de build
│   └── windows/                # Manifesto Windows, ícone, installer NSIS
│
├── DOCs/                       # Documentação técnica detalhada
│   ├── P2P_SERVER_API_IMPLEMENTATION.md
│   ├── p2p-api-contract.md
│   ├── WATCHDOG_SYSTEM.md
│   ├── MULTI_USER_SERVICE_GUIDE.md
│   ├── INSTALADOR_PAYLOAD_E_PARAMETROS.md
│   └── ANALISE_DEPENDENCIAS.md
│
├── main.go                     # Entry point: detecta modo (GUI/service/MCP)
├── service_main.go             # Inicialização do Windows Service headless
├── tray_embed.go               # Embed do ícone ICO (deve ficar na raiz por go:embed)
├── startup_debug_keys_windows.go # Detecta Shift/Ctrl na inicialização (Windows)
├── startup_debug_keys_other.go   # Stub para outras plataformas
├── go.mod                      # Dependências Go
├── wails.json                  # Config do Wails (build, nome, ícones)
└── ARCHITECTURE.md             # Este arquivo
```

---

## 3. Diagrama de Dependências

```
main.go ──────────────────────────────────── startup_debug_keys_*.go
   │
   ├──► package app (app/)
   │         │
   │         ├── App struct (app.go)
   │         │      ├── startup/shutdown lifecycle
   │         │      └── Wails context management
   │         │
   │         ├── Subpacotes extraídos (app/debug, app/inventory, app/support, app/updates, app/netutil)
   │         │      ├── debug.Service: credenciais, bootstrap, InstallerConfig
   │         │      ├── inventory.Service: coleta, cache, sync payloads
   │         │      ├── support.Service: tickets, knowledge base
   │         │      └── updates.Service: winget upgrade, exporter
   │         │
   │         ├── Bridges (app/*_bridge.go)
   │         │      └── Delegam para subpacotes, expõem métodos ao frontend/MCP
   │         │
   │         ├── Notificações e Comandos
   │         │      ├── notification_center.go: DispatchNotification (Wails events)
   │         │      ├── command_result_outbox.go: outbox pattern para resultados
   │         │      └── consolidation_engine.go: janelas de consolidação de eventos
   │         │
   │         ├── P2P domain (p2p.go, p2p_*.go)
   │         │      ├── p2pCoordinator: descoberta + replicação + gossip
   │         │      ├── p2pTransferServer: HTTP local P2P
   │         │      ├── p2pChunkScheduler: swarm downloads
   │         │      ├── p2pPeerCache: cache persistente de peers
   │         │      ├── Providers: mDNS, UDP, libp2p (DHT)
   │         │      ├── Cloud bootstrap: seed externo via servidor
   │         │      └── Onboarding: boot sem servidor
   │         │
   │         └── Shared (chat.go, sync.go, store.go, ...)
   │
   ├──► internal/agentconn    # Conexão servidor (NATS/WS + dispatch de comandos)
   ├──► internal/ai           # LLM chat service
   ├──► internal/automation   # Motor de automação
   ├──► internal/buildinfo    # Versão de build para pacotes internos
   ├──► internal/database     # SQLite KV cache
   ├──► internal/inventory    # Coleta de inventário
   ├──► internal/mcp          # MCP server + registry + ping
   ├──► internal/models       # Tipos compartilhados
   ├──► internal/selfupdate   # Auto-atualização do binário
   ├──► internal/services     # Wrappers de serviços
   └──► internal/watchdog     # Health monitoring

service_main.go ──► internal/service (ServiceManager headless)
```

---

## 4. Índice Completo de Arquivos

### Raiz

| Arquivo | Responsabilidade |
|---------|-----------------|
| `main.go` | Entry point. Detecta flag `--service` ou inicia GUI Wails. Modo standalone `--mcp` removido. |
| `service_main.go` | Inicializa o modo Windows Service headless. Configura logging em arquivo, cria ServiceManager. |
| `tray_embed.go` | Apenas o `//go:embed` do ícone `.ico`. Deve ficar na raiz: `//go:embed` não permite paths com `..`. Os bytes são passados ao App via `AppStartupOptions.TrayIcon`. |
| `startup_debug_keys_windows.go` | Detecta Shift/Ctrl pressionados no startup via `GetAsyncKeyState` (Windows API). |
| `startup_debug_keys_other.go` | Stub que retorna `false` em plataformas não-Windows. |

### Package `app/`

`app/` está organizado por domínio, com subpacotes Go reais extraídos progressivamente a partir do `package app`. O `package app` principal ainda mantém o hub central (`App` struct) e os arquivos de bridge que delegam para os subpacotes.

#### Core

| Arquivo | Responsabilidade |
|---------|-----------------|
| `app.go` | `App` struct central. Campos de estado, `NewApp()`, `startup()`, `shutdown()`, `beginActivity()`, lifecycle helpers. |
| `types.go` | Tipos compartilhados usados por quase todo o pacote: `AppStartupOptions`, `RuntimeFlags`, `P2PConfig`, `P2PMetrics`, views de UI e tipos auxiliares. |
| `bridge.go` | Implementa a interface `AppBridge` esperada por `internal/mcp/register.go`. Converte resultados internos → JSON para ferramentas MCP. |
| `logging.go` | `logBuffer`: ring buffer thread-safe, persistência opcional em arquivo e sanitização de tokens. |
| `memory.go` | CRUD de notas locais via SQLite. |

#### Subpacotes extraídos de `app/`

| Subpacote | Arquivo | Responsabilidade |
|-----------|---------|-----------------|
| `app/debug` | `config.go` | `Config` (DebugConfig): credenciais de conexão, flags TLS, NATS, InstallerConfig. |
| `app/debug` | `service.go` | `Service`: bootstrap de token/agentID, persistência multi-path, status de conectividade. |
| `app/debug` | `installer.go` | `installerConfigPathCandidates()` + parser de `InstallerConfig`. |
| `app/inventory` | `service.go` | `Service`: coleta de inventário, installs/upgrades, cache, notificações para UI. |
| `app/inventory` | `sync.go` | Constrói payloads `agentHardwareEnvelope`/`agentSoftwareEnvelope` para POST no servidor. |
| `app/support` | `service.go` | `Service`: criação de tickets, normalização de prioridade, serialização para API. |
| `app/support` | `knowledge.go` | Fetch e cache de artigos da knowledge base. |
| `app/updates` | `service.go` | `Service`: `GetPendingUpdates()` — executa `winget upgrade` e parseia saída. |
| `app/updates` | `export.go` | `Exporter`: exportação de relatório de atualizações pendentes. |
| `app/netutil` | `headers.go` | `SetAgentAuthHeaders()` — aplica headers de autenticação usados pelas APIs. |
| `app/appstore` | `types.go` | `Item`, `Response`, `EffectivePolicy`, `PolicyCache` do app store. |
| `app/automation` | `types.go` | `StateView`, `TaskView`, `ExecutionView` — tipos de UI da automação. |
| `app/p2pmeta` | `types.go` | Config, views, telemetry, onboarding, seed-plan e cache leve do domínio P2P. |
| `app/supportmeta` | `types.go` | `AgentInfo`, workflow/tickets, knowledge base e cache leve. |
| `app` | `headless_p2p_runtime.go` | `HeadlessP2PService`: sobe o coordinator P2P e a telemetria sem Wails para o Windows Service. |

#### Bridges (delegates para subpacotes)

| Arquivo | Responsabilidade |
|---------|-----------------|
| `debug_bridge.go` | `GetDebugConfig`/`SetDebugConfig` — delega a `debug.Service`. |
| `inventory_bridge.go` | `GetCatalog` — delega a `inventory.Service`. |
| `inventory_export.go` | `getInventoryForExport` — helper que obtém inventário do cache ou coleta novo. |
| `logs_bridge.go` | `GetLogs`/`ClearLogs` — delega ao ring buffer interno. |
| `p2p_bridge.go` | `applyP2PConfig` — aplica nova config P2P ao coordinator. |
| `support_bridge.go` | `GetAgentInfo` e demais métodos de suporte — delega a `support.Service`. |
| `support_log.go` | `supportLogf` — helper de logging do domínio de suporte. |
| `updates_bridge.go` | `GetPendingUpdates` — delega a `updates.Service`. |
| `updates_helpers.go` | `parseUpgradeOutput` — wrapper sobre `updates.ParseUpgradeOutput`. |
| `export_redact.go` | `getRedact()` — expõe flag de redação de PII para o export pipeline. |

#### Configuração e Conexão

| Arquivo | Responsabilidade |
|---------|-----------------|
| `agent_config.go` | `AgentConfiguration`: struct e loader (parse do JSON de políticas do servidor). |
| `agent_config_helpers.go` | `GetAgentConfiguration` e helpers de acesso a configuração do agente. |
| `installer.go` | `installerConfigPathCandidates` — wrapper/bridge no package app para paths de config. |
| `sync.go` | `syncCoordinator`: polling de manifest do servidor, download incremental de resources e deduplicação de eventos. |

#### Inventário e App Store

| Arquivo | Responsabilidade |
|---------|-----------------|
| `store.go` | Fetch e cache do app store (`AppStoreResponse`). Cache em memória + SQLite + ETag. |
| `printers.go` | Listagem, instalação e remoção de impressoras. |

#### Automação

| Arquivo | Responsabilidade |
|---------|-----------------|
| `automation.go` | Bridge entre `internal/automation.Service` e frontend. Traduz estado e enums para a UI. |
| `automation_p2p.go` | `automationPackageManagerRouter`: decide se instalação usa P2P (swarm), HTTP ou winget nativo. |

#### Notificações e Comandos

| Arquivo | Responsabilidade |
|---------|-----------------|
| `notification_center.go` | `NotificationDispatchRequest`, `DispatchNotification()`: envio e aguardo de resposta do usuário via Wails eventos. |
| `command_result_outbox.go` | Outbox pattern para resultados de comandos: deduplicação SHA-256, retry com timestamp. |
| `consolidation_engine.go` | `ConsolidationEngine`: janelas de consolidação de eventos (realtime / 1min / 5min). |

#### Debug e Diagnóstico

| Arquivo | Responsabilidade |
|---------|-----------------|
| `remote_debug.go` | `remoteDebugManager`: sessão de debug remoto com streaming de logs do agente. |
| `psadt_debug_bridge.go` | Bootstrap e diagnóstico do PSADT (PowerShell App Deploy Toolkit). |

#### P2P

| Arquivo | Responsabilidade |
|---------|-----------------|
| `p2p.go` | `p2pCoordinator`: núcleo do P2P, lifecycle, estado e orquestração. |
| `headless_p2p_runtime.go` | `HeadlessP2PService`: wrapper headless que reutiliza `p2pCoordinator` no modo service. |
| `p2p_api.go` | Chamadas ao servidor para seed planning e telemetry P2P. |
| `p2p_chunks.go` | `p2pChunkScheduler`: divide artefatos em chunks, distribui entre peers e aplica bandwidth cap. |
| `p2p_cleanup.go` | Limpeza de artefatos expirados e arquivos temporários do diretório P2P. |
| `p2p_cloud_bootstrap.go` | Bootstrap via servidor externo: obtém peers iniciais quando não há descoberta local. |
| `p2p_config.go` | Helpers de configuração P2P: normalização, valores default e validação. |
| `p2p_discovery.go` | `p2pDiscoveryProvider` interface + providers: `p2pMDNSProvider` e UDP broadcast. |
| `p2p_download.go` | Download de artefatos de um único peer e helpers de verificação. |
| `p2p_gossip.go` | Gossip pull: `pullPeerGossip()` — sincronização do índice de artefatos entre peers via libp2p. |
| `p2p_http.go` | `p2pTransferServer`: HTTP local para servir artefatos e receber onboarding. |
| `p2p_libp2p.go` | `p2pLibP2PProvider` e `p2pMultiProvider`: integração libp2p com DHT, bootstrap estático (hybrid/libp2p-only). |
| `p2p_libp2p_transport.go` | Transport layer libp2p (stream handling). |
| `p2p_onboarding.go` | `p2pOnboardingState`: loop de onboarding para agentes sem servidor. Retry com backoff exponencial. |
| `p2p_peer_cache.go` | Cache persistente de peers conhecidos (até 128 entradas) entre reinicializações. |
| `p2p_publish.go` | `ListArtifacts` e publicação de artefatos para os peers. |
| `p2p_replication.go` | Workers de replicação: `replicateArtifactToPeerNow()` — push com validação e token auth. |
| `p2p_status.go` | Views de status P2P para UI (peers, artefatos, modo). |
| `p2p_telemetry_outbox.go` | Outbox de telemetria P2P: batch de eventos enviados ao servidor central. |

#### UI, Apresentação e Suporte

| Arquivo | Responsabilidade |
|---------|-----------------|
| `chat.go` | Config de chat, histórico em memória, streaming e integração com `internal/ai` e `internal/mcp`. |
| `status.go` | `GetStatusOverview()` → `StatusOverview`: snapshot de saúde geral para a UI. |
| `mesh.go` | Instalação automática do MeshCentral Agent quando habilitado no servidor. |
| `tray.go` | System tray: ícone, menu, heartbeat watchdog e ações de janela. |
| `ui_runtime.go` | Heartbeat, suspensão e recovery do runtime da UI integrados ao watchdog. |
| `ui_runtime_windows.go` | Probe nativo Win32 (`EnumWindows`, `IsHungAppWindow`) para validar responsividade da janela principal. |
| `ui_runtime_other.go` | Fallback cross-platform quando o probe nativo não existe. |
| `tray_text_other.go` | Wrappers de `systray.Set*` para plataformas não-Darwin. |
| `tray_text_darwin.go` | Wrappers de `systray.Set*` para Darwin. |

### Package `internal/`

| Pacote | Arquivo | Responsabilidade |
|--------|---------|-----------------|
| `agentconn` | `runtime.go` | Conexão persistente com servidor central (NATS/WebSocket). Config loader, reconexão automática, callbacks de ping e dispatch de comandos. |
| `ai` | `chat.go` | `Service`: SendMessage/StreamMessage para LLM OpenAI-compatible. Tool calling via MCP registry. |
| `automation` | `service.go` | Motor de automação: sync de políticas, cron scheduling, execução de tasks, callback polling. |
| `automation` | `executor.go` | Executa scripts PowerShell/Batch com timeout e captura de output. |
| `automation` | `client.go` | HTTP client para API de automação com headers de autenticação. |
| `automation` | `types.go` | Tipos: `AutomationTask`, `AutomationScript`, `State`, `PSADTPolicy`, enums de escopo e ação. |
| `buildinfo` | `version.go` | `Version`: variável de versão para consumo por pacotes internos (complementa `app.Version`). |
| `ctxutil` | `timeout.go` | Helpers de timeout/cancelamento de contexto. |
| `data` | `http_client.go` | `HTTPClient`: GET com cache ETag, SQLite cache, retry, timeout configurável. |
| `database` | `sqlite.go` | `DB`: KV store com TTL (`CacheGetJSON`/`CacheSetJSON`), CRUD de `MemoryNote`. Schema: `kv_cache`, `memory_notes`. |
| `export` | `markdown.go` | Renderer Markdown de `InventoryReport` via template Go. |
| `export` | `pdf.go` | Renderer PDF de `InventoryReport` via biblioteca fpdf. |
| `export` | `redaction.go` | `RedactHardware()`: mascara seriais, MACs, passwords, tokens. |
| `inventory` | `provider.go` | `Provider`: orquestra coleta — osquery preferred, PowerShell como fallback. Progress callback para UI. |
| `inventory` | `osquery.go` | Subprocess-based query runner (fallback) + cache do binário osqueryi. |
| `inventory` | `osquery_client.go` | osquery-go Thrift client: conecta ao socket do osqueryd (se disponível) ou inicia osqueryi em modo socket para executar todas as queries via uma única conexão, sem overhead de subprocesso por query. |
| `inventory` | `powershell.go` | Queries WMI/CIM via `powershell.exe` (fallback quando osquery não disponível). |
| `inventory` | `mappers.go` | Mapeia resultados brutos de queries → `models.InventoryReport`. |
| `mcp` | `server.go` | `Server`: JSON-RPC 2.0 via stdio. Endpoints: `initialize`, `tools/list`, `tools/call`. |
| `mcp` | `tools.go` | `Registry`, `Tool`, `Handler`. Registro, dispatch e conversão de schemas. |
| `mcp` | `register.go` | `RegisterDiscoveryTools()` + interface `AppBridge`. Registra todas as ferramentas Discovery. |
| `mcp` | `ping.go` | Ferramenta MCP de ping/diagnóstico de conectividade. |
| `models` | `inventory.go` | `InventoryReport`: schema universal de inventário. `HardwareInfo`, `SoftwareItem`, `UpgradeItem`, `PrinterInfo`, etc. |
| `printer` | `manager.go` | `Manager`: list/install/remove impressoras via Windows Print Spooler. |
| `processutil` | `(util.go)` | Executa processos externos, captura stdout/stderr, timeout. |
| `selfupdate` | `updater.go` | `Updater`: download de nova versão do binário, verificação SHA-256 e substituição atômica do executável. |
| `service` | `client.go` | `ServiceClient`: cliente da Named Pipe para a UI consultar status e saúde do service. |
| `service` | `ipc.go` | `IPCServer`: protocolo de comandos e respostas do Windows Service via Named Pipe. |
| `service` | `manager.go` | `ServiceManager`: entry point do modo headless. Gerencia watchdog, IPC e workers. |
| `service` | `runtime_services.go` | Adapters de runtime que conectam automação/inventário reais ao modo service. |
| `services` | `apps_service.go` | `AppsService`: wrapper de `winget.Client` para install/uninstall/upgrade. |
| `services` | `catalog_service.go` | `CatalogService`: fetch e cache do catálogo de aplicativos. |
| `services` | `inventory_service.go` | `InventoryService`: coleta com timeout e progress callback. |
| `services` | `printer_service.go` | `PrinterService`: wrapper do `printer.Manager`. |
| `watchdog` | `watchdog.go` | `Watchdog`: heartbeat por componente, timeout de inatividade, recuperação de goroutine panic. |
| `winget` | `client.go` | `Client`: executa `winget` commands e faz parse da saída tabular no formato Windows. |

### Frontend `frontend/`

| Arquivo | Responsabilidade |
|---------|-----------------|
| `index.html` | Shell HTML principal. Chrome da janela customizado (drag bar, min/max/close). |
| `app.js` | Bootstrap da aplicação, view de catálogo/packages. Expõe `getApp()` → `window.go.app.App`. |
| `styles.css` | Estilos globais (variáveis CSS, layout, componentes). |
| `js/app-init.js` | Event bindings iniciais (cards, search, botões de tab). |
| `js/app-status.js` | Polling de status de conexão e versão. |
| `js/app-inventory.js` | Visualização de inventário em árvore/tabela. |
| `js/app-p2p.js` | Painel P2P: peers, artefatos, configuração. |
| `js/app-chat.js` | Interface de chat AI: input, histórico, streaming. |
| `js/app-automation.js` | Painel de automação: tasks, scripts, status. |
| `js/app-store-updates.js` | Lista de updates pendentes (winget upgrade). |
| `js/app-support.js` | Formulário e listagem de tickets de suporte. |
| `js/app-debug.js` | Painel de configuração de conexão/debug. |
| `js/app-utils.js` | Debounce, formatação de bytes, helpers DOM. |
| `js/app-window.js` | Chrome handlers: min/max/close, status bar polling. |
| `wailsjs/go/app/App.js` | Bindings JS gerados pelo Wails (não editar manualmente). |
| `wailsjs/runtime/` | Runtime Wails (events, window, dialog — não editar). |

---

## 5. Fluxos Principais

### Startup (GUI Mode)

```
main() 
  └─► NewApp(opts)           // inicializa todos os serviços
  └─► wails.Run()
        └─► app.startup(ctx)
              ├─► a.startTray(icon)     // system tray
              ├─► database.Open()       // SQLite
              ├─► collectInventory()    // goroutine
              ├─► agentConn.Run()       // goroutine: conexão servidor
              ├─► syncCoord.Run()       // goroutine: sync de políticas
              ├─► p2pCoord.Run()        // goroutine: P2P discovery
              ├─► automationSvc.Run()   // goroutine: cron + callbacks
              └─► meshCentral install   // se habilitado
```

### P2P Artifact Distribution

```
p2pCoordinator.Run()
  ├─► discoveryTick()              // mDNS/UDP/libp2p: descobre peers
  ├─► p2pTransferServer.Start()   // HTTP server local (porta 41080-41120)
  └─► replicationWorkers (x2)
        └─► replicateArtifactToPeerNow()
              ├─► validatePeer()
              ├─► fetchArtifactFromLocal()
              └─► pushToRemotePeer()     // HTTP PUT com token auth

// Download (swarm ou single):
DownloadArtifactSwarm()
  └─► p2pChunkScheduler.Schedule()
        ├─► splitIntoChunks()
        ├─► assignChunksToPeers()
        └─► downloadParallel() + bandwidth cap
```

### Agent Onboarding (sem servidor)

```
p2pOnboardingState.RunOnboardingLoop()
  ├─► isAgentConfigured()? → skip
  ├─► GetActivePeers()
  ├─► requestOnboardingFromPeer()   // GET /p2p/config/onboard
  │     └─► HMAC-SHA256 validation
  └─► persistCredentials()
```

### MCP Tool Calling (Interno)

```
UI Chat interno → ai.Service
  └─► tools/call {name, args}
        └─► Registry.Call(name, args)
              └─► AppBridge.{method}()  // app.bridge.go
```

---

## 6. Padrões de Código

### Onde adicionar cada tipo de funcionalidade

| Tipo de mudança | Onde implementar |
|----------------|-----------------|
| Nova feature de UI (método Go para frontend) | `app/{dominio}_bridge.go` ou `app/{dominio}.go` — método em `*App `|
| Nova ferramenta MCP | `internal/mcp/register.go` (interface) + `app/bridge.go` (implementação) |
| Nova query de inventário | `internal/inventory/osquery.go` ou `powershell.go` + mapper em `mappers.go` |
| Nova lógica de inventário (negócio) | `app/inventory/service.go` ou `app/inventory/sync.go` |
| Nova fonte de dados P2P | `app/p2p.go` (coordinator) + se necessário `app/p2p_discovery.go` |
| Nova política de automação | `internal/automation/types.go` (tipo) + `service.go` (execução) |
| Persistência de novo dado | `internal/database/sqlite.go` (schema + métodos) |
| Novo export format | `internal/export/` (renderer) + `app/updates/export.go` (orquestrador) |
| Nova configuração de debug/conexão | `app/debug/config.go` (campo) + `app/debug/service.go` (lógica) |
| Auto-atualização do binário | `internal/selfupdate/updater.go` |
| Nova notificação para usuário | `app/notification_center.go` (`DispatchNotification`) |

### Convenções

- **Métodos de UI** (chamados pelo JS): receptor `*App`, exportados, retornam tipos serializáveis JSON
- **Goroutines**: usar `watchdog.SafeGoWithContext()` para monitoramento e recuperação de panics
- **Logging**: `a.logs.append("[modulo] mensagem")` — ring buffer acessível via `GetLogs()` no frontend
- **Config persistence**: SQLite `CacheSetJSON` / `CacheGetJSON` — nunca gravar credenciais em texto simples sem necessidade
- **Cross-domain calls**: `app/` pode importar qualquer `internal/`; `internal/` não deve importar `app/`
- **Organização física do `app/`**: manter arquivos no mesmo diretório enquanto forem `package app`. Separação em subpastas exige subpacotes Go e refatoração explícita de imports e bindings.

---

## 8. Build e Desenvolvimento

### Comandos

```bash
# Desenvolvimento (hot-reload)
wails dev

# Build produção (Windows)
wails build -platform windows/amd64

# Testes Go
go test ./...

# Verificação de erros
go build ./...
```

### ldflags (versão)

A variável `Version` principal está em `discovery/app`. O pacote `internal/buildinfo` expõe uma cópia para uso por pacotes internos. Ao fazer build com versão explícita:

```bash
wails build -ldflags "-X discovery/app.Version=1.2.3 -X discovery/internal/buildinfo.Version=1.2.3"
# ou (go build direto)
go build -ldflags "-X discovery/app.Version=1.2.3 -X discovery/internal/buildinfo.Version=1.2.3"
```

### Wails JS Bindings

Os arquivos em `frontend/wailsjs/go/app/` são **gerados automaticamente** pelo Wails ao rodar `wails dev` ou `wails build`. O namespace JS é `window.go.app.App.*`.

---

## 7. Dependências Notáveis

| Dependência | Uso |
|-------------|-----|
| `wails/v2` | Framework GUI desktop (runtime, bindings, window management) |
| `energye/systray` | System tray cross-platform |
| `libp2p/go-libp2p` | Stack P2P (DHT, mDNS, TCP/QUIC transport) |
| `grandcat/zeroconf` | mDNS discovery (modo legacy P2P) |
| `robfig/cron/v3` | Cron scheduling para automação |
| `nats-io/nats.go` | Comunicação com servidor central via NATS |
| `modernc.org/sqlite` | SQLite driver (pure Go, sem CGO) |
| `go-pdf/fpdf` | Geração de PDFs |
| `samber/lo` | Utilities funcionais (map, filter, etc.) |
| `google/uuid` | Geração de UUIDs para IDs de peers/artifacts |
