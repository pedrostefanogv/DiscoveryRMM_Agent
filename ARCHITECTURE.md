# Discovery Agent — Arquitetura e Índice de Código

> **Propósito deste documento:** Guia de referência rápida para desenvolvedores e agentes de IA. Descreve a estrutura do projeto, responsabilidades de cada arquivo e como os componentes se relacionam.

---

## 1. Visão Geral

O **Discovery Agent** é um aplicativo de inventário e gerenciamento de TI para Windows. Ele combina:

- **UI Desktop** via [Wails v2](https://wails.io/) (frontend HTML/JS + backend Go)
- **Windows Service** headless (sem UI) para execução em background
- **Servidor MCP** stdio para integração com Claude Desktop e outros clientes LLM
- **Rede P2P** para distribuição de artefatos (instaladores, políticas) entre agentes na mesma rede local
- **Automação** de tarefas via políticas baixadas do servidor central

### Modos de Execução

| Modo | Trigger | Descrição |
|------|---------|-----------|
| GUI (Wails) | padrão | Janela desktop com system tray |
| Windows Service | `--service` | Headless, sem UI, roda como serviço |
| MCP Server | `--mcp` | JSON-RPC stdio para LLM tool calling |

---

## 2. Estrutura de Diretórios

```
discovery/
├── app/                        # Pacote principal da aplicação (package app)
│   ├── app.go                  # App struct — hub central, lifecycle
│   ├── types.go                # Tipos compartilhados (App/P2P/export structs)
│   ├── logging.go              # logBuffer — ring buffer em memória + arquivo
│   ├── status.go               # StatusOverview — health snapshot para UI
│   ├── memory.go               # CRUD de notas locais (memory notes)
│   ├── bridge.go               # AppBridge — expõe métodos App ao MCP registry
│   ├── chat.go                 # Chat AI: config, histórico, streaming, MCP tools
│   ├── export.go               # Export de inventário em Markdown e PDF
│   ├── mesh.go                 # Integração MeshCentral (remote management)
│   ├── support.go              # Tickets de suporte: criação, prioridades
│   ├── updates.go              # Parser de `winget upgrade` output
│   ├── store.go                # App store catalog + cache (ETag + SQLite)
│   ├── printers.go             # Gerenciamento de impressoras
│   ├── sync.go                 # syncCoordinator — sync de políticas com servidor
│   ├── agent_config.go         # AgentConfiguration — config baixada do servidor
│   ├── debug.go                # DebugConfig, credenciais de conexão, bootstrap
│   ├── tray.go                 # System tray (icon, menu, heartbeat)
│   ├── tray_text_other.go      # Wrappers systray (non-Darwin)
│   ├── tray_text_darwin.go     # Wrappers systray (Darwin)
│   ├── inventory_sync.go       # Constrói payloads de inventário para o servidor
│   ├── inventory_ops.go        # Operações de inventário (coleta, cache, osquery)
│   ├── automation.go           # Wrapper de automação → frontend UI
│   ├── automation_p2p.go       # Router de pacotes: P2P vs HTTP vs winget
│   ├── p2p.go                  # p2pCoordinator — núcleo do sistema P2P
│   ├── p2p_http.go             # Servidor HTTP P2P para transferência de artefatos
│   ├── p2p_chunks.go           # Scheduler de downloads em chunks/swarm
│   ├── p2p_discovery.go        # Provedores de descoberta: mDNS, UDP
│   ├── p2p_libp2p.go           # Provider libp2p + modo hybrid/libp2p-only
│   ├── p2p_libp2p_transport.go # Transport layer libp2p
│   ├── p2p_onboarding.go       # Loop de onboarding para agents sem servidor
│   └── p2p_api.go              # API do servidor: seed plan, telemetry P2P
│
├── internal/                   # Pacotes internos (lógica sem dependência do App)
│   ├── agentconn/              # Conexão NATS/WebSocket com servidor central
│   │   └── runtime.go          # Runtime de conexão: config, reconexão, ping
│   ├── ai/                     # Serviço de chat AI (LLM OpenAI-compatible)
│   │   ├── chat.go             # Service: SendMessage, StreamMessage, tool calling
│   │   └── formatting_test.go  # Testes de formatação de mensagens
│   ├── automation/             # Motor de automação (políticas, scripts, cron)
│   │   ├── service.go          # Service: sync de políticas, execução, callbacks
│   │   ├── executor.go         # Executa scripts PowerShell/Batch/Shell
│   │   ├── client.go           # HTTP client para API de automação
│   │   └── types.go            # Tipos: Task, Script, ExecutionRecord, State
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
│   │   ├── osquery.go          # Queries osquery: CPU, RAM, disk, software, etc.
│   │   ├── powershell.go       # Queries WMI/CIM via PowerShell (fallback Windows)
│   │   ├── helpers.go          # Utilitários internos de parsing
│   │   └── mappers.go          # Mapeia raw query results → models.InventoryReport
│   ├── mcp/                    # MCP (Model Context Protocol) server e registry
│   │   ├── server.go           # JSON-RPC 2.0 server (stdio)
│   │   ├── tools.go            # Registry, Tool, Handler abstractions
│   │   ├── register.go         # RegisterDiscoveryTools + interface AppBridge
│   │   └── ping_test.go        # Testes de ping MCP
│   ├── models/                 # Tipos de dados compartilhados
│   │   └── (inventory.go, etc) # InventoryReport, HardwareInfo, SoftwareItem, etc.
│   ├── printer/                # Gerenciamento de impressoras
│   │   └── manager.go          # List, install, remove impressoras
│   ├── processutil/            # Utilitários de processo
│   │   └── (util.go)           # Run command, capture output, etc.
│   ├── service/                # Windows Service (modo headless)
│   │   └── service.go          # ServiceManager: ciclo de vida do service
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
   │         ├── Connection domain (debug.go, agent_config.go)
   │         │      ├── DebugConfig: credenciais servidor
   │         │      └── AgentConfiguration: políticas do servidor
   │         │
   │         ├── P2P domain (p2p.go, p2p_*.go)
   │         │      ├── p2pCoordinator: descoberta + replicação
   │         │      ├── p2pTransferServer: HTTP local P2P
   │         │      ├── p2pChunkScheduler: swarm downloads
   │         │      ├── Providers: mDNS, UDP, libp2p
   │         │      └── Onboarding: boot sem servidor
   │         │
   │         ├── Inventory domain (inventory_ops.go, inventory_sync.go)
   │         │      ├── Coleta com fallback (osquery → PowerShell)
   │         │      └── Sync payloads para servidor
   │         │
   │         ├── Automation domain (automation.go, automation_p2p.go)
   │         │      ├── Wrapper de políticas para UI
   │         │      └── Router: P2P vs HTTP vs winget
   │         │
   │         └── Shared (chat.go, export.go, sync.go, store.go, ...)
   │
   ├──► internal/agentconn    # Conexão servidor (NATS/WS)
   ├──► internal/ai           # LLM chat service
   ├──► internal/automation   # Motor de automação
   ├──► internal/database     # SQLite KV cache
   ├──► internal/inventory    # Coleta de inventário
   ├──► internal/mcp          # MCP server + registry
   ├──► internal/models       # Tipos compartilhados
   ├──► internal/services     # Wrappers de serviços
   └──► internal/watchdog     # Health monitoring

service_main.go ──► internal/service (ServiceManager headless)
```

---

## 4. Índice Completo de Arquivos

### Raiz

| Arquivo | Responsabilidade |
|---------|-----------------|
| `main.go` | Entry point. Detecta flags `--service`, `--mcp`, ou inicia GUI Wails. Define `runMCPServer()` e helpers de argumento. |
| `service_main.go` | Inicializa o modo Windows Service headless. Configura logging em arquivo, cria ServiceManager. |
| `tray_embed.go` | Apenas o `//go:embed` do ícone `.ico`. Deve ficar na raiz: `//go:embed` não permite paths com `..`. Os bytes são passados ao App via `AppStartupOptions.TrayIcon`. |
| `startup_debug_keys_windows.go` | Detecta Shift/Ctrl pressionados no startup via `GetAsyncKeyState` (Windows API). |
| `startup_debug_keys_other.go` | Stub que retorna `false` em plataformas não-Windows. |

### Package `app/`

| Arquivo | Responsabilidade |
|---------|-----------------|
| `app.go` | `App` struct central. Campos de estado, `NewApp()`, `startup()`, `shutdown()`, `beginActivity()`, lifecycle helpers. |
| `types.go` | Todos os tipos expostos ao frontend: `AppStartupOptions`, `RuntimeFlags`, `P2PConfig`, `P2PBootstrapConfig`, `P2PSeedPlan`, `P2PMetrics`, `P2PAuditEvent`, `AgentInfo`, `exportConfig`, `APIWorkflowState`, etc. |
| `logging.go` | `logBuffer`: ring buffer thread-safe (5k linhas), persistência opcional em arquivo, redação de tokens. |
| `status.go` | `GetStatusOverview()` → `StatusOverview`: snapshot de saúde geral (conexão, versão, idade do inventário). |
| `memory.go` | CRUD de notas locais (`GetLocalMemories`, `AddLocalMemory`, `DeleteLocalMemory`) via SQLite. |
| `bridge.go` | Implementa a interface `AppBridge` esperada por `internal/mcp/register.go`. Converte resultados internos → JSON para ferramentas MCP. |
| `chat.go` | Config de chat (`ChatConfig`, `ChatMessage`), histórico em memória, streaming, integração com `internal/ai`. |
| `export.go` | `ExportInventoryMarkdown()` e `ExportInventoryPDF()`: busca inventário, redação de PII, escrita em Downloads. |
| `mesh.go` | Instalação automática do MeshCentral Agent quando habilitado no servidor. |
| `support.go` | Criação de tickets de suporte, normalização de prioridades e status. |
| `updates.go` | `GetPendingUpdates()`: executa `winget upgrade` e faz parse da saída tabular com tratamento de spinner CR. |
| `store.go` | Fetch e cache do app store (`AppStoreResponse`). Cache de 2 min em memória + 30 dias SQLite + ETag. |
| `printers.go` | Listagem, instalação e remoção de impressoras. |
| `sync.go` | `syncCoordinator`: polling de manifest do servidor, download incremental de resources (revision-based), deduplicação de eventos. |
| `agent_config.go` | `AgentConfiguration`: parse, cache e aplicação de configurações baixadas do servidor (feature flags, P2P policy, timeouts). |
| `debug.go` | `DebugConfig`: credenciais de conexão. Bootstrap de token/agentID. Persistência multi-path. Registro via deploy token. |
| `tray.go` | System tray: ícone, menu (Abrir/Sair), heartbeat watchdog, click handlers. Recebe icon bytes via parâmetro. |
| `tray_text_other.go` | Wrappers de `systray.Set*` para plataformas não-Darwin. |
| `tray_text_darwin.go` | Wrappers de `systray.Set*` para Darwin. |
| `inventory_ops.go` | Operações de inventário: `GetInventory()` (cache+heartbeat), `RefreshInventory()`, `Install/Uninstall/Upgrade`, `GetOsqueryStatus()`. |
| `inventory_sync.go` | Constrói `agentHardwareEnvelope` e `agentSoftwareEnvelope` para POST no servidor. Filtra e normaliza campos. |
| `automation.go` | Bridge entre `internal/automation.Service` e frontend. Traduz enums → labels UI. |
| `automation_p2p.go` | `automationPackageManagerRouter`: decide se install usa P2P (swarm) vs HTTP vs winget nativo. |
| `p2p.go` | `p2pCoordinator`: núcleo do P2P. Descoberta de peers, replicação, swarm download, deduplicação, audit trail. |
| `p2p_http.go` | `p2pTransferServer`: HTTP local para servir artefatos (autenticação por HMAC token). Endpoint de onboarding. |
| `p2p_chunks.go` | `p2pChunkScheduler`: divide artefatos em chunks, distribui entre peers paralelos, bandwidth cap. |
| `p2p_discovery.go` | `p2pDiscoveryProvider` interface + providers: `p2pMDNSProvider` (zeroconf) e UDP broadcast. |
| `p2p_libp2p.go` | `p2pLibP2PProvider` e `p2pMultiProvider`: integração libp2p com DHT, bootstrap estático, modos hybrid/libp2p-only. |
| `p2p_libp2p_transport.go` | Transport layer libp2p (stream handling). |
| `p2p_onboarding.go` | `p2pOnboardingState`: loop de onboarding para agentes genéricos. Retry com backoff exponencial. |
| `p2p_api.go` | Chamadas ao servidor para seed planning e telemetry P2P. Loop de telemetry em background. |

### Package `internal/`

| Pacote | Arquivo | Responsabilidade |
|--------|---------|-----------------|
| `agentconn` | `runtime.go` | Conexão persistente com servidor central (NATS/WebSocket). Config loader, reconexão automática, callbacks de ping. |
| `ai` | `chat.go` | `Service`: SendMessage/StreamMessage para LLM OpenAI-compatible. Tool calling via MCP registry. |
| `automation` | `service.go` | Motor de automação: sync de políticas, cron scheduling, execução de tasks, callback polling. |
| `automation` | `executor.go` | Executa scripts PowerShell/Batch com timeout e captura de output. |
| `automation` | `client.go` | HTTP client para API de automação com headers de autenticação. |
| `automation` | `types.go` | Tipos: `AutomationTask`, `AutomationScript`, `State`, enums de escopo e ação. |
| `ctxutil` | `timeout.go` | Helpers de timeout/cancelamento de contexto. |
| `data` | `http_client.go` | `HTTPClient`: GET com cache ETag, SQLite cache, retry, timeout configurável. |
| `database` | `sqlite.go` | `DB`: KV store com TTL (`CacheGetJSON`/`CacheSetJSON`), CRUD de `MemoryNote`. Schema: `kv_cache`, `memory_notes`. |
| `export` | `markdown.go` | Renderer Markdown de `InventoryReport` via template Go. |
| `export` | `pdf.go` | Renderer PDF de `InventoryReport` via biblioteca fpdf. |
| `export` | `redaction.go` | `RedactHardware()`: mascara seriais, MACs, passwords, tokens. |
| `inventory` | `provider.go` | `Provider`: orquestra coleta — osquery preferred, PowerShell como fallback. Progress callback para UI. |
| `inventory` | `osquery.go` | Queries osquery: CPU, RAM, discos, printers, software, rede, battery, BitLocker, etc. |
| `inventory` | `powershell.go` | Queries WMI/CIM via `powershell.exe` (fallback quando osquery não disponível). |
| `inventory` | `mappers.go` | Mapeia resultados brutos de queries → `models.InventoryReport`. |
| `mcp` | `server.go` | `Server`: JSON-RPC 2.0 via stdio. Endpoints: `initialize`, `tools/list`, `tools/call`. |
| `mcp` | `tools.go` | `Registry`, `Tool`, `Handler`. Registro, dispatch e conversão de schemas. |
| `mcp` | `register.go` | `RegisterDiscoveryTools()` + interface `AppBridge`. Registra todas as ferramentas Discovery. |
| `models` | `inventory.go` | `InventoryReport`: schema universal de inventário. `HardwareInfo`, `SoftwareItem`, `PrinterInfo`, etc. |
| `printer` | `manager.go` | `Manager`: list/install/remove impressoras via Windows Print Spooler. |
| `processutil` | `(util.go)` | Executa processos externos, captura stdout/stderr, timeout. |
| `service` | `service.go` | `ServiceManager`: entry point do modo headless. Ciclo de vida sem Wails. |
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

### MCP Tool Calling

```
main --mcp → runMCPServer()
  └─► mcp.Server.Start(stdin/stdout)
        └─► tools/call {name, args}
              └─► Registry.Call(name, args)
                    └─► AppBridge.{method}()  // app.bridge.go
```

---

## 6. Padrões de Código

### Onde adicionar cada tipo de funcionalidade

| Tipo de mudança | Onde implementar |
|----------------|-----------------|
| Nova feature de UI (método Go para frontend) | `app/{dominio}.go` — método em `*App` |
| Nova ferramenta MCP | `internal/mcp/register.go` (interface) + `app/bridge.go` (implementação) |
| Nova query de inventário | `internal/inventory/osquery.go` ou `powershell.go` + mapper em `mappers.go` |
| Nova fonte de dados P2P | `app/p2p.go` (coordinator) + se necessário `app/p2p_discovery.go` |
| Nova política de automação | `internal/automation/types.go` (tipo) + `service.go` (execução) |
| Persistência de novo dado | `internal/database/sqlite.go` (schema + métodos) |
| Novo export format | `internal/export/` (renderer) + `app/export.go` (orquestrador) |

### Convenções

- **Métodos de UI** (chamados pelo JS): receptor `*App`, exportados, retornam tipos serializáveis JSON
- **Goroutines**: usar `watchdog.SafeGoWithContext()` para monitoramento e recuperação de panics
- **Logging**: `a.logs.append("[modulo] mensagem")` — ring buffer acessível via `GetLogs()` no frontend
- **Config persistence**: SQLite `CacheSetJSON` / `CacheGetJSON` — nunca gravar credenciais em texto simples sem necessidade
- **Cross-domain calls**: `app/` pode importar qualquer `internal/`; `internal/` não deve importar `app/`

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

A variável `Version` foi movida para `discovery/app`. Ao fazer build com versão explícita:

```bash
wails build -ldflags "-X discovery/app.Version=1.2.3"
# ou
go build -ldflags "-X discovery/app.Version=1.2.3"
```

### Wails JS Bindings

Os arquivos em `frontend/wailsjs/go/app/` são **gerados automaticamente** pelo Wails ao rodar `wails dev` ou `wails build`. O namespace JS é `window.go.app.App.*`.

> ⚠️ **Limpeza pós-reorganização:** Os arquivos `_deleted_*.go` na raiz do projeto podem ser deletados com segurança — todos têm `//go:build ignore` e não afetam o build. São resquícios da migração para o `package app/`.

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
