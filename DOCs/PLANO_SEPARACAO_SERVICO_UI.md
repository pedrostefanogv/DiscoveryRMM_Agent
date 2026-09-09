# Plano: Separação Definitiva Serviço (Core) × Interface do Usuário

> Status: **EXECUTADO + 5 REVISÕES + §0.5–§0.9 (2026-09-08) — 10 bugs corrigidos; MIGRAÇÃO FÍSICA COMPLETA (lotes 1+2); restante: checklist em VM real**
> Data: 2026-09-08 (revisão 5 — auditoria do lote 2; revisões 1–4 e execução: 2026-09-08)
> Base: `DOCs/PLANO_AGENT_SERVICE_SYSTEM.md` (Fases 0–2 já implementadas via flag `ServiceMode`; revisão de 2026-09-04 recomenda extração `CoreAgent` — este plano executa essa recomendação)

## 0.9 Quinta revisão — auditoria do lote 2 (2026-09-08)

Auditoria completa da migração lote 2 (10 categorias: renames escapados, métodos minúsculos cross-package, campos internos, duplicidade de tipos, atribuições, métodos do LogBuffer, testes, bridges, arquivos P2P, shutdown). **1 bug real + 2 limpezas:**

| #   | Achado                                                                                                                                                                                                                   | Impacto                                                                           | Correção                                                                                     |
| --- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------- |
| B10 | `coreagent.RuntimeFlags` **perdeu as tags json** (`debugMode`/`startMinimized`/`serviceMode:"-"`) na migração — o struct original do app as tinha e o frontend consome `GetRuntimeFlags()` serializado                   | Frontend deixaria de receber `debugMode`/`startMinimized` (campos vazios no JSON) | Tags restauradas em `coreagent_lote2.go`                                                     |
| L1  | `logBuffer` antigo ainda definido em `logging.go` (código morto — o campo `Logs` usa `coreagent.LogBuffer`)                                                                                                              | Duplicidade/confusão                                                              | Bloco removido; `logging.go` mantém apenas `sanitizeToken`/`truncateLogBody`/`captureStdLog` |
| L2  | Structs agrupadores mortos no `coreagent_lote2.go` (`P2PState`, `StartupState`, `ActivityState`, `CoreAtoms`, `DeferredRestartState`) — os campos foram achatados direto no `CoreAgent` e `deferredRestart` ficou no App | Código morto; campo `DeferredRestart` órfão no CoreAgent                          | Structs removidos; campo `DeferredRestart` removido do CoreAgent (fica no App, documentado)  |

**Checagens que NÃO encontraram problema:**

- Renames — nenhum `a.<campo-minúsculo>` escapado (case-sensitive, inclui receivers `app.*`/`m.app.*`).
- Métodos minúsculos cross-package — nenhum uso restante (`a.Logs.Append` etc. corretos).
- Campos internos (`InvCache.mu`, `AppStorePolicy.inner`) — nenhum acesso direto.
- `Subscribe`/`SnapshotAndSubscribe` — promovidos do `*logs.Buffer` embutido no `LogBuffer` (falso alarme inicial).
- Mutexes exportados (`P2PMu`/`StartupMu`/`ActivityMu`) — 23 usos Lock/Unlock consistentes.
- Testes, bridges (`ipc_rpc`/`companion_bridges`/`logs_bridge`/`automation`/`updates_bridge`/`status`), arquivos P2P (7) e `shutdown()` — todos com nomes novos.
- `GetRuntimeFlags` — converte corretamente entre tipos (alias `RuntimeFlags = coreagent.RuntimeFlags`).
- B8 (`updates:list`) — evento emitido no `ipcRPCUpdatesScan` confirmado.

**Validação:** `go build ./...` OK · `go vet ./app/...` OK · `gofmt` limpo · `go test ./app/... -count=1` **32 pacotes, 100% verdes** · guardrail `TestCoreAgentNoWailsImports` OK.

## 0.8 Migração física — lote 2 (2026-09-08)

Conclusão da migração física `App` → `coreagent.CoreAgent` (a pré-condição "mover `SyncDeps`/`AppDeps` para os pacotes de domínio" já estava satisfeita — as interfaces vivem em `app/sync/deps.go` e `app/p2p/deps.go` por design de inversão de dependência).

**Tipos movidos para `app/coreagent/coreagent_lote2.go`:**

| Tipo (antigo no app)   | Novo (coreagent)      | Notas                                                                                                                 |
| ---------------------- | --------------------- | --------------------------------------------------------------------------------------------------------------------- |
| `logBuffer`            | `LogBuffer`           | Wrapper de `logs.Buffer`; métodos minúsculos viraram exportados (Append/GetAll/Subscribe/Count/EnableFilePersistence) |
| `inventoryCache`       | `InventoryCache`      | + wrappers Get/Set/Has/Reset (Reset usado por `clearMemoryCaches`)                                                    |
| `exportConfig`         | `ExportConfig`        | + wrappers Get/Set (usados por `getRedact`/`SetRedact` do Exporter)                                                   |
| `agentInfoCache`       | `AgentInfoCache`      | wrapper de `supportmeta.AgentInfoCache`                                                                               |
| `appStorePolicyCache`  | `AppStorePolicyCache` | + `CachePointer()` (usado por `automation.RuntimeConfig.Cache`)                                                       |
| `RuntimeFlags`         | `RuntimeFlags`        | + `StartMinimized`; alias local no app preserva as tags json do frontend                                              |
| `deferredRestartState` | — (ficou no App)      | Estado do power-command de UI; usa campos minúsculos intensivamente — migração não compensa                           |

**Campos promovidos no embed (renome mecânico `a.<campo>` → `a.<Campo>`):** `Logs`, `InvCache`, `ExportCfg`, `AgentInfo`, `AppStorePolicy`, `P2PCoord` (alias `p2pCoordinator`), `P2PMu`, `P2PConfig`, `P2PSeedPlanCache`, `P2PTelemetryRateLimitUntil`, `StartupMu/Err/Wg`, `ActivityMu`, `ActiveOps`, `LastIdle`, `IdleKnown`, `IdleCapable`, `RuntimeFlags`.

**Ficaram no App (UI/bridge com dependência de `*App`):** `packageManagerRouter` (recebe `*App` no construtor), `mcpRegistry`, `chatSvc`, `psadtSvc`, `debugHTTP*`, tray, `closeMu/allowClose`, zero-touch, `ipcServer/ipcClient`, `companionStatus`, `deferredRestart`.

**Limpeza:** tipos duplicados mortos removidos de `types.go`/`logging.go` (funções órfãs `inventoryCache`/`exportConfig` apagadas; `captureStdLog` ajustado para `*logs.Buffer`; aliases `AppStore*` e `RuntimeFlags` preservados no app).

**Validação:** `go build ./...` OK · `go vet ./app/...` OK · `gofmt` limpo · `go test ./app/... -count=1` **32 pacotes, 100% verdes** · guardrail `TestCoreAgentNoWailsImports` OK.

## 0.7 Quarta revisão — auditoria pós-migração lote 1 (2026-09-08)

Auditoria completa da migração física (§0.6) e das entregas §0.5 — subagent verificou 7 categorias (renames escapados, campos não-movidos, atribuições, shutdown, testes, duplicidade de embed, bridges) + auditoria manual dos fluxos RPC. **2 bugs encontrados e corrigidos:**

| #   | Bug                                                                                                                                                                                     | Impacto                                                                                           | Correção                                                                                                                                                                           |
| --- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| B8  | `ipcRPCUpdatesScan` prometia (em comentário) emitir evento `updates:list` quando o scan excede o timeout do RPC (10s), mas **o evento nunca era emitido** — e o frontend não o escutava | Scan winget/choco lento (>10s): a UI recebe timeout, o resultado se perde e a página fica em erro | Serviço emite `updates:list` após o scan (`ipc_rpc.go`); frontend escuta o evento em `app-store-updates.js` e renderiza a entrega tardia (mesmo padrão de `store:catalog-updated`) |
| B9  | Campo `CoreAgent.Ctx` era **morto** (nunca atribuído/lido) com comentário incorreto ("setado em SetContext" — SetContext seta `a.ctx` do App, não o campo)                              | Confusão para o lote 2; risco de alguém "usar" um campo sempre nil                                | Campo removido do `coreagent.CoreAgent` (o ciclo de vida continua no App via `a.ctx`/`Ctx()`; mover para o core é decisão do lote 2)                                               |

**Checagens que NÃO encontraram problema (verificadas):**

- Renames da migração — nenhum `a.<campo-minúsculo>` remanescente em ~36 arquivos (inclui receivers `m.app.*`/`app.*`); campos não-movidos (`p2pCoord`, `logs`, `invCache`, `chatSvc`, `psadtSvc`, `packageManagerRouter`, `agentInfo`, `appStorePolicy`) intactos.
- Atribuições em `NewApp`/`startup`/`runCoreStartup` — todos os 33 campos movidos atribuídos com nome novo; `DB`/`ConsolEngine` corretamente em startup.
- Shutdown — fecha `SyncSvc`, `p2pCoord`, `InventorySvc`, `CoreAgent.DB`, `logs` com nomes novos; sem referências órfãs.
- Sombreamento de embed — nenhum campo local do App colide com promovidos (exceto o intencional `DB()`/`Ctx()` métodos).
- `GetLogCount`/`ExportLogs` em companion — `exportLogs` do frontend usa as linhas já carregadas via `GetLogs` (que busca do serviço), consistente.
- `ipcRequest` usa `a.ctx` (não o campo morto) — correto.
- Guardrail `TestCoreAgentNoWailsImports` — passa (grafo de deps de coreagent sem wails).

**Validação:** `go build ./...` OK · `go vet` OK · `gofmt` limpo · `go test ./app/... -count=1` 100% ok · `node --check` no JS do listener OK.

## 0.3 Segunda revisão de bugs (2026-09-08)

Auditoria completa da entrega + revisão 1 — 3 novos bugs encontrados e corrigidos:

| #   | Bug                                                                                                                                                                                                                                                                                                                                                           | Impacto                                                                                            | Correção                                                                                                                                                                                                                                                                         |
| --- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| B4  | **Código morto no serviço**: `a.ctx` nunca é nil (sempre `context.WithCancel`), então `s.ctx() == nil` era sempre falso → o caminho headless do `notifications.Service.Dispatch` (onde vive o `NativeFallback`/toast) **nunca executava no serviço**. Sem UI conectada: notify_only não mostrava toast nenhum e require_confirmation expirava silenciosamente | Decisão D4 (toast nativo) completamente inoperante em runtime — o bug mais grave das duas revisões | `notifications.Deps.Ctx` injetado em `NewApp` agora retorna nil quando `ServiceMode && (ipcServer == nil \|\| ClientCount() == 0)` — i.e., o serviço sem UI conectada é tratado como headless e cai no toast nativo; com UI companion conectada, mantém `a.ctx` (render via IPC) |
| B5  | Mesmo no caminho headless, `require_confirmation` retornava `timeout_policy_applied` **imediatamente** — o usuário não tinha janela de tempo para clicar nos botões do toast (o `Respond` via hook de ativação chegava depois do retorno)                                                                                                                     | require_confirmation via toast nunca poderia ser aprovado, mesmo com B4 corrigido                  | Caminho headless reescrito: para `require_confirmation`, registra `pendingNotifyResult` e aguarda resposta via `select` com `TimeoutSeconds` — mesma semântica do caminho com UI. `notify_only` headless permanece `approved`/`headless_logged`                                  |
| B6  | `IPCClient.Request` mutava o map do caller ao injetar `payload["method"] = method`                                                                                                                                                                                                                                                                            | Hoje inofensivo (callers passam literais), mas armadilha para callers que reutilizam payload       | Cópia defensiva do payload antes de injetar `method`                                                                                                                                                                                                                             |

**Testes novos (`service_ui_separation_test.go`):**

- `TestDispatchHeadlessRequireConfirmationWaitsForToastResponse` — valida B5: resposta via `Respond` (caminho do hook de toast) dentro da janela de timeout é aceita como `user_decision`.
- `TestDispatchHeadlessNotifyOnlyApproved` — notify_only headless: `approved`/`headless_logged` + `NativeFallback` invocado.
- `TestDispatchWithContextNoNativeFallback` — com UI presente, `NativeFallback` não dispara; `emitEvent` assume.

**Checagens que NÃO encontraram problema (verificadas nesta revisão):**

- `handleIPCRequest` delete de `payload["method"]` — mutação por-conn, seguro (confirmado na revisão 1).
- Stubs não-Windows (`ipc_other.go`, `native_toast_other.go`) — `ipcRequest`/`ipc_rpc.go`/`companion_rpc.go` não referenciam tipos windows-only; compilação `!windows` íntegra.
- `getHeartbeatMetrics` — `UIOnline` só preenchido com `ServiceMode && ipcServer != nil` (nil-safe).
- `dispatchNativeToastWhenHeadless` — parse de `Metadata["actions"]` como `[]any` consistente com JSON round-trip do IPC.
- Shutdown — `ipcServer.Close()`/`ipcClient.Close()` com guards nil, idempotentes (`CompareAndSwap`).
- `TestUpsertAndListAutomationDeferState` (database) — flaky intermitente conhecido, passa 5× seguidas isolado; não relacionado.

**Validação:** `go build ./...` OK · `go vet ./app/...` OK · `go test ./app/... -count=1` 100% ok (incluindo os 3 testes novos).

## 0.4 Melhorias em testes (2026-09-08)

Expansão da cobertura dos caminhos IPC RPC e do ciclo headless — novo arquivo `app/ipc_rpc_test.go`:

| Teste                                            | O que cobre                                                                                                                                         |
| ------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------- |
| `TestHandleIPCRequestNilGuards`                  | App nil / payload nil sem panic                                                                                                                     |
| `TestHandleIPCRequestUnknownMethod`              | Método desconhecido → `ok=false` com mensagem de erro                                                                                               |
| `TestHandleIPCRequestMemorySvcIndisponivel`      | `memory:list/add/delete` com `memorySvc` nil → `ok=false` (sem panic)                                                                               |
| `TestHandleIPCRequestPendingCountsNoDB`          | `status:pending_counts` sem DB (companion D3) → `ok=true` com data vazio                                                                            |
| `TestHandleIPCRequestDeletesMethodKey`           | Contrato: handler consome `payload["method"]` antes do switch                                                                                       |
| `TestHandleIPCMessageNilGuards`                  | Roteamento `handleIPCMessage` nil-safe para todos os tipos de mensagem (hello, status, remote_session, notification:respond, request, desconhecido) |
| `TestDispatchHeadlessRequireConfirmationTimeout` | B5: headless sem resposta aguarda a janela de `TimeoutSeconds` (valida que NÃO é retorno imediato)                                                  |
| `TestRespondAfterPendingExpiry`                  | `Respond` após expiração do dispatch → `false` (canal de pending removido no defer)                                                                 |
| `TestRespondNormalizesResult`                    | `Respond("ADIADO")` → normalizado para `deferred` no resultado do Dispatch (variação de caixa/acento)                                               |
| `TestDispatchIdempotencyKeyDeduplicates`         | Mesma `IdempotencyKey` → segunda chamada `deduplicated`                                                                                             |

**Bug B7 encontrado pelo teste** `TestHandleIPCMessageNilGuards`: `IPCServer.Broadcast` com receiver nil dava panic (`s.mu.Lock()` em ponteiro nulo) — atingível no serviço quando `ipcServer == nil` em `IPCMsgRemoteSession`/handshake. Corrigido com guard nil-safe no início de `Broadcast` (mesmo padrão de `Close`).

**Validação:** `go build ./...` OK · `go vet ./app/...` OK · `go test ./app/... -count=1` 100% ok (10 testes novos + B7 corrigido).

## 0.2 Revisão de bugs (2026-09-08)

**Limitação conhecida remanescente (aceita):** toasts WinRT disparados por serviço SYSTEM na sessão 0 podem não exibir botões de ação em algumas build Windows (limitação da plataforma). Nesses casos o fluxo cai no timeout de `require_confirmation` — comportamento seguro por padrão.

## 0.2 Revisão de bugs (2026-09-08)

Auditoria do código entregue — 3 bugs reais encontrados e corrigidos:

| #   | Bug                                                                                                                                                                                                 | Impacto                                                             | Correção                                                                                                                                                                                                                                                                                           |
| --- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| B1  | `TestRunCoreContextCancellation` chamava `RunCore()` no teste unitário → abria `discovery.db` real em `ProgramData\Discovery` e registrava o named pipe IPC (side effects de ambiente em `go test`) | Contaminação do ambiente de dev/CI                                  | Teste agora roda só com `DISCOVERY_INTEGRATION_TESTS=1` (skip por padrão; coberto pelo checklist manual)                                                                                                                                                                                           |
| B2  | `setToastActivationHook` nunca era chamado → cliques nos botões de ação do toast eram descartados; `require_confirmation` via toast **nunca** seria respondido                                      | Funcionalidade D4 incompleta                                        | Hook conectado em `startup()` do modo serviço: `setToastActivationHook(...)` roteia `notification:<id>:<result>` → `notificationSvc.Respond`                                                                                                                                                       |
| B3  | **Violação da decisão D3**: no modo companion a UI abria o SQLite do serviço (`database.Open` antes do check companion)                                                                             | `SQLITE_BUSY` intermitente — o problema que D3 existe para eliminar | `startup()`: bloco de DB agora roda **apenas** quando `!companion`; guards nil verificados em todos os consumers (`appstore` corrigido: `s.db != nil` não bastava pois `s.db()` retorna nil — agora `s.db() != nil`; `support`/`debug`/`http_client`/`status`/`sync`/`agent_config` já protegidos) |

**Melhoria adicional na revisão:** `GetStatusOverview` no companion agora preenche `PendingCommandResults`/`PendingP2PTelemetry` via RPC `status:pending_counts` (antes ficariam zerados sem o DB).

**Checagens que NÃO encontraram problema (verificadas):**

- `handleIPCRequest` mutando o payload do request — seguro: payload é por-conn, não compartilhado.
- `IPCClient.Request` com conexão caindo entre Send e resposta — canal pendente é limpo no defer; timeout cobre.
- `canInstallNow` no serviço — já tratado (`ServiceMode → true`).
- `un.StopAndWaitAgentService` — já espera STOPPED com loop 30s.
- `EmitEvent` headless → `broadcastIPCEvent` — nil-safe (testado).
- Usos restantes de `a.db` no companion — todos com guard `a.db != nil`/`a.db == nil`.
- Teste flaky `TestDeferredStateDeadlineExhaustion` (automation) — passou 10× seguidas; falha isolada foi flakiness de timing, não relacionada.

## 0.5 Execução das pendências (2026-09-08)

Implementação das melhorias não bloqueantes listadas em §0.1 — 4 de 5 concluídas:

| #   | Melhoria                                               | Status | Entregas                                                                                                                                                                                                                                                                                                                                                                                                          |
| --- | ------------------------------------------------------ | ------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 4   | Handshake IPC versionado (`protocol`)                  | ✅     | `IPCProtocolVersion = 2` (`ipc_windows.go`); `hello` envia `protocol` e `hello_ack` responde com `service` + `protocol`; `probeServiceHello()` retorna `IPCServiceHelloAck{Service, Protocol}`; `IsServiceProtocolCompatible()` valida faixa (serviços antigos sem campo = protocolo 1, aceitos); UI antiga + serviço com protocolo > suportado → modo standalone com log (evita crash por drift durante updates) |
| 2   | RPCs `logs:tail` / `automation:state` / `updates:scan` | ✅     | Handlers no lado do serviço (`ipc_rpc.go`): `ipcRPCTailLogs` (default 100, teto 2000), `ipcRPCAutomationState` (AutomationStateView serializado), `ipcRPCUpdatesScan` (winget/choco roda NO serviço — D2). Bridges da UI (`companion_bridges.go`): `getTailLogsCompanion`, `getAutomationStateCompanion`, `getPendingUpdatesCompanion` — todos caem no caminho local em standalone                                |
| 3   | `CompanionController` testável                         | ✅     | `companion_controller.go`: interface `CompanionTuner` (SendStatus/OnServiceLost/OnFallbackStandalone) + `CompanionConfig` (interval 5s, 60 falhas); loop extraído de `startIPCClient`; adapter `companionTunerAdapter` conecta a App. Testável sem pipe real (fakeTuner + ticks manuais)                                                                                                                          |
| 5   | Backend consumir `uiOnline`                            | ✅     | `DiscoveryRMM_API`: `AgentHeartbeat.UiOnline` (DTO), `HeartbeatCacheEntry.UiOnline`, cópia em `HeartbeatCacheService.SetHeartbeatAsync`, `heartbeat.UiOnline` no eventPayload do dashboard (`NatsAgentMessaging`), `HeartbeatMetricsDto.UiOnline` + `MapToDto` (exposição na API). Retrocompatível (null quando ausente)                                                                                          |
| 1   | Migração física `App` → `coreagent.CoreAgent`          | ⏳     | Refactor mecânico grande (mover campos core do god-object App); fronteira segue documental + guardrails. Fica para sessão dedicada                                                                                                                                                                                                                                                                                |

**Testes novos:** `app/ipc_protocol_test.go` (13 testes: protocolo/handshake, tail-logs, nil-guards dos RPCs, CompanionController com `-race` — reset de falhas no sucesso, fallback após MaxFailures, cancelamento de contexto, não-reentrância; bridges standalone). `AgentHeartbeatParsingTests.cs` no backend (3 testes NUnit: uiOnline true/false/ausente retrocompatível).

**Validação:** agente — `go build ./...` OK · `go vet` OK · `go test ./app/... -count=1` 100% ok (testes database flaky preexistente passam isolados). Backend — `dotnet build Discovery.slnx` OK · `dotnet test` (parsing uiOnline) 3/3 ok.

## 0.6 Migração física (lote 1), bridges conectados e checklist E.2 (2026-09-08)

### Migração física `App` → `coreagent.CoreAgent` — lote 1 ✅

**Estratégia:** embedding incremental — `App` incorpora `coreagent.CoreAgent` e a promoção de campos mantém todos os acessos existentes (refactor mecânico, zero mudança de comportamento).

- **Lote 1 (concluído):** campos cujos tipos vivem em pacotes externos ao `app` — `DB`, `CatalogSvc`, `CatalogClient`, `AppsSvc`, `InvSvc`, `PrinterSvc`, `AutomationSvc`, `SyncSvc`, `AgentConn`, `DebugSvc`, `AgentConfigSvc`, `TicketsSvc`, `UpdatesSvc`, `Exporter`, `InventorySvc`, `SupportSvc`, `ConsolEngine`, `HardwareIDSvc`, `MemorySvc`, `NotificationSvc`, `ApiClientSvc`, `CustomFieldsSvc`, `AppStoreSvc`, `SelfUpdater`, `SelfUpdaterCh`, `UpdateTrigger`, `RemoteDebug`, `RemoteSessionMgr`, `AgentConfig(+Mu)`, `QueuedForceHeartbeat`, `QuitRequested` — agora em `app/coreagent/coreagent.go` (33 campos, 22 imports de domínio, zero Wails).
- **Renames mecânicos:** `a.<campo>` → `a.<Campo>` (exportados, exigência do embed cross-package) aplicados a 36 arquivos do package app + testes. Colisões resolvidas: método `App.DB()` (interface `sync.SyncDeps`) agora retorna `a.CoreAgent.DB`; struct literals em testes usam `CoreAgent: coreagent.CoreAgent{...}`.
- **Guardrail novo:** `coreagent_import_guardrail_test.go` — `go list -deps` valida que o grafo de imports de coreagent NUNCA contém wails (falha o teste se regredir). Doc.go atualizado com o estado do lote 1/lote 2.
- **Lote 2 (pendente):** campos de tipos definidos no package app (`p2pCoordinator`, `logBuffer`, caches, `automationPackageManagerRouter`, `RuntimeFlags`, `agentInfoCache`) — exigem mover os tipos primeiro para quebrar ciclos de import (pré-requisito mapeado: `sync.SyncDeps`/`p2p.AppDeps` para os domínios).

### Bridges RPC conectados às telas ✅

Métodos Wails-bound chamados pelo frontend agora delegam ao serviço via RPC em companion mode (caminho local em standalone):

| Método (frontend)      | Companion (RPC)           | Standalone (local)    |
| ---------------------- | ------------------------- | --------------------- |
| `GetPendingUpdates()`  | `updates:scan` (D2)       | `updatesSvc` local    |
| `GetAutomationState()` | `automation:state`        | `automationSvc` local |
| `GetLogs()`            | `logs:tail` (2000 linhas) | `logBuffer` local     |

### Checklist manual E.2 ✅ (documento criado)

`DOCs/CHECKLIST_FASE_E2_VALIDACAO_MANUAL.md` — 9 blocos de validação em máquina real: instalação limpa (binPath/D3 handles), boot sem login, `uiOnline` nos 3 estados, notificações/toast nos 4 caminhos, RPCs (updates/automation/logs/memory/pending counts), fallback standalone (5min), update in-place, inventário SYSTEM, desinstalação. Critérios de aceite resumidos ao final.

**Validação:** `go build ./...` OK · `go vet ./...` OK · `gofmt` limpo · `go test ./app/... -count=1` 100% ok (incluindo `app/coreagent` com o guardrail de imports).

## 0.1 Registro de execução (2026-09-08)

| Fase                    | Status                                       | Entregas                                                                                                                                                                                                                                                                                                                                                                     |
| ----------------------- | -------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| C2 — uiOnline           | ✅                                           | `AgentHeartbeat.UIOnline *bool` (`core/agentconn/runtime.go`); `AgentHeartbeatMetrics.UIOnline`; populado em `app.go getHeartbeatMetrics()` quando `ServiceMode && ipcServer != nil`                                                                                                                                                                                         |
| C2 — Toast nativo       | ✅                                           | `app/native_toast_windows.go` (go-toast WinRT, actions com correlation `notification:<id>:<result>`); hook `NativeFallback` em `notifications.Service` (dispara toast quando `ctx==nil` e `ServiceMode`); stub `native_toast_other.go`                                                                                                                                       |
| B — Binário do serviço  | ✅                                           | `cmd/discovery-service/main.go` (novo binário, sem Wails no caminho de execução); `RunCore(ctx)` desacopla `ServiceStartup` do tipo Wails; `main.go` UI recusa rodar como serviço (`os.Exit(1)`); Taskfile `build:service` (sem syso/frontend); NSIS copia `discovery-service.exe` e aponta `binPath` para ele                                                               |
| C — IPC RPC             | ✅                                           | `IPCMsgRequest/IPCMsgResponse` + `CorrelationID` no envelope; `IPCClient.Request(ctx, method, payload)` com timeout 10s e roteamento por cid no readLoop; handler `handleIPCRequest` (métodos `status:pending_counts`, `config:get`, `inventory:snapshot`, `memory:list/add/delete`) em `ipc_rpc.go`; Memory Notes da UI companion via RPC (`companion_rpc.go`, `memory.go`) |
| D — NSIS/update         | ✅                                           | `SERVICE_EXECUTABLE` define; `wails.files` copia os 2 binários; `sc create binPath= discovery-service.exe` (+ fallback `sc config` p/ instalações antigas com `--service`); taskkill do binário do serviço em `PrepareForInPlaceUpdate` e uninstall (`StopAndWaitAgentService` já esperava STOPPED); `CanInstallNow` sempre true no serviço                                  |
| A — Fronteira CoreAgent | ✅ (fronteira formal) / ⏳ (migração física) | `RunCore(ctx)` como único caminho de startup do core; `app/coreagent/doc.go` documenta a fronteira; guardrails em `coreagent_guardrail_test.go` (ServiceMode nunca inicializa Wails; EmitEvent headless safe); migração física dos campos do App para struct `CoreAgent` no pacote fica como refactor mecânico posterior                                                     |
| E — Testes              | ✅                                           | `service_ui_separation_test.go` (uiOnline nil fora do serviço, correlation-id round-trip, toast nil-safe) + `coreagent_guardrail_test.go`; `go build ./...` OK; `go test ./app/... -count=1` 100% ok                                                                                                                                                                         |

**Pendências (atualizado após §0.5):**

1. ~~Lote 2 da migração física~~ → **concluído em §0.8**: tipos (`LogBuffer`, caches, `RuntimeFlags`, `agentInfo/appStorePolicy` wrappers) e campos (`Logs`, `InvCache`, `ExportCfg`, `P2PCoord`, P2P-state, startup/activity state, `RuntimeFlags`) movidos para o coreagent. Ficaram no App apenas UI/bridges com dependência de `*App`.
2. **Checklist manual (Fase E.2)**: executar `DOCs/CHECKLIST_FASE_E2_VALIDACAO_MANUAL.md` numa VM real (9 blocos: instalação, boot sem login, uiOnline, notificações, RPCs, fallback, update in-place, inventário, desinstalação). Requer deploy — decisão do usuário.
3. Nota: o `cmd/discovery-service` ainda linka o package `app` inteiro (que importa Wails para a UI). A migração física está completa (lotes 1+2) — a eliminação do peso no link agora exige mover os bridges Wails-bound do `App` para um package separado (fora do escopo; o guardrail `coreagent_import_guardrail_test.go` impede regressão de fronteira).
4. ~~Conectar bridges ao frontend~~ → **concluído em §0.6**: `GetPendingUpdates`, `GetAutomationState` e `GetLogs` delegam via RPC em companion.
5. ~~Melhorias futuras~~ → **concluídas em §0.5**: RPCs `logs:tail`/`automation:state`/`updates:scan`, `companion.Controller` testável, handshake versionado (`protocol: 2`), backend consome `uiOnline`.

## 0. Decisões aprovadas (2026-09-08)

| #   | Decisão                  | Escolha                                                                                         |
| --- | ------------------------ | ----------------------------------------------------------------------------------------------- |
| D1  | Binários                 | **Dois binários**: `discovery-service.exe` (serviço) + `discovery-agent.exe` (UI)               |
| D2  | Winget/updates pendentes | **Roda no serviço**; a UI apenas exibe os dados (via IPC) e dispara ações                       |
| D3  | SQLite                   | **DB único, dono = serviço**; UI companion NUNCA abre o arquivo (detalhes na §2.3)              |
| D4  | Toast nativo             | Preparar notificação nativa do Windows (go-toast/Win32) para uso quando não houver UI conectada |
| D5  | `uiOnline` no heartbeat  | Incluir campo no heartbeat do serviço (servidor sabe se há UI ativa)                            |

---

## 1. Situação Atual e Problemas

O mesmo binário `discovery-agent.exe` roda em 3 modos (`main.go`):

1. **Serviço Windows** (`--service`, `RunServiceMode` em `app/servicemode_service_windows.go`) — LocalSystem, sessão 0, via flag `runtimeFlags.ServiceMode` que pula os itens de UI dentro de `App.startup()`.
2. **UI Companion** (sessão do usuário) — detecta serviço via IPC (`decideCompanionMode`, `ipc_app_integration.go`), roda só UI/tray/chat.
3. **UI Standalone** (fallback) — core completo no processo do usuário.

### Problemas identificados

| #   | Problema                                                                                                                                                                         | Evidência no código                                  |
| --- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------- |
| P1  | **God-object `App`**: um único struct (~1796 linhas em `app.go`, + bridges) concentra core, UI, tray, Wails, IPC. Toda manutenção e teste é arriscada.                           | `app/Core… app.go:55-231` (campos), `startup():1137` |
| P2  | **Modo serviço usa o mesmo `App`**, apenas "pulando" código de UI via `if ServiceMode`. Qualquer esquecimento vira bug na sessão 0 (ex.: caminho `runas`/UAC, WebView2, janela). | `servicemode_service_windows.go`, `app.go:1167`      |
| P3  | **Binário do serviço carrega a UI**: frontend embedado (`//go:embed all:frontend`), WebView2, tray e Wails são linkados no processo SYSTEM — peso e superfície desnecessários.   | `main.go:13`                                         |
| P4  | **Duplo caminho de startup**: `startup()` (UI) e `runCoreStartup()` (serviço) duplicam a abertura de DB e wiring de serviços — drift garantido.                                  | `app.go:1137-1270`                                   |
| P5  | **Duplicação de lógica de eventos**: `EmitEvent` bifurca Wails vs IPC (`app.go:876-884`); cada domínio precisa saber dos dois mundos.                                            | `broadcastIPCEvent` em `ipc_app_integration.go:85`   |
| P6  | **Fallback companion duplicado**: `startIPCClient` replica polling de status/regra de 5min — lógica de orquestração embutida na UI.                                              | `ipc_app_integration.go:208-260`                     |
| P7  | **Companion abre o mesmo SQLite** do serviço (WAL + busy_timeout mitigam, mas acesso concorrente permanece).                                                                     | plano anterior, Fase 0                               |
| P8  | **Self-update mata serviço e UI** juntos (`taskkill /IM discovery-agent.exe`) — janelas de race já mapeadas no plano anterior (Fase 3/4).                                        | `project.nsi`                                        |

---

## 2. Arquitetura Alvo

```
┌──────────────────────────────────────────────────────────────┐
│ discovery-service.exe (Windows Service, LocalSystem, sessão 0)│
│  package coreagent (NOVO — extraído do package app)           │
│   ├─ agentConn (NATS/heartbeat)        [dono]                 │
│   ├─ inventory + sync                  [dono]                 │
│   ├─ automation + PSADT                [dono]                 │
│   ├─ P2P coordinator                   [dono]                 │
│   ├─ self-update                       [dono]                 │
│   ├─ outboxes + consolidation engine   [dono]                 │
│   ├─ notifications (persist/headless)  [dono]                 │
│   └─ IPC server (named pipe)           [dono]                 │
└──────────────────────────────────────────────────────────────┘
                              │ IPC JSON-lines (named pipe, SDDL SY+BU)
┌──────────────────────────────────────────────────────────────┐
│ discovery-agent.exe (UI Wails, sessão do usuário)             │
│   ├─ janela + tray + idle mode                                  │
│   ├─ chat AI + MCP + SSE                                        │
│   ├─ notificações interativas (renderiza eventos do serviço)    │
│   ├─ remote session / terminal (sessão interativa)              │
│   ├─ IPC client (companion) com fallback standalone             │
│   └─ CoreAgent embarcado (usado SÓ no modo standalone)          │
└──────────────────────────────────────────────────────────────┘
```

### Decisões de arquitetura

| Decisão                   | Escolha (aprovada)                                                                                                                                                                                            | Alternativa descartada                                        |
| ------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------- |
| D1 — Binários             | **Dois binários** (`discovery-service.exe` + `discovery-agent.exe`), dois `cmd` no mesmo módulo Go, compartilhando `coreagent`. Serviço leve (sem WebView2/embed), update independente, menos risco sessão 0. | Binário único (serviço carrega UI morta)                      |
| D2 — Extração `CoreAgent` | Extrair o core para `src/app/coreagent/` como pacote próprio, **sem nenhuma referência a Wails**. `App` (UI) compõe `CoreAgent` no standalone.                                                                | Continuar com flag `ServiceMode` (status quo do plano Fase 1) |
| D3 — Comunicação IPC      | Contrato atual (JSON-lines) + **extensão de métodos RPC** para tudo que a UI companion precisa (status, inventário cached, config, comandos interativos).                                                     | gRPC/HTTP local (mais pesado)                                 |
| D4 — Frontend             | Sem mudanças estruturais: `window.go.app.App.*` continua; os bridges Go consultam o CoreAgent local (standalone) ou IPC client (companion), transparente para o JS.                                           | —                                                             |
| D5 — Instalador           | Dois executáveis no NSIS; update do serviço usa `sc stop` → wait STOPPED → replace → `sc start` (ordem já documentada).                                                                                       | —                                                             |

### 2.3 Arquitetura de dados — SQLite (D3, analisado no código)

**Análise feita:** a API de `core/database/sqlite.go` é quase 100% core — outboxes (command_result, p2p_telemetry), automation (callbacks, markers, defer states, executions), inventário (snapshots, `ShouldSyncInventory`), consolidação, notificações persistidas, action queue, cache KV (`agent_configuration_raw`). Os únicos usos pela UI são:

1. Leitura do cache `agent_configuration_raw` (`agent_config_app.go:126`) — dado do core.
2. Contadores de status (`status.go:107` — `CountPendingCommandResultOutbox`/`CountPendingP2PTelemetryOutbox`) — dado do core.
3. **Memory Notes** (`memory.go` → `ListMemoryNotes`/CRUD) — única feature genuinamente de UI.

**Arquitetura escolhida: DB único, dono = serviço.**

- O serviço é o **único processo** que abre `C:\ProgramData\Discovery\discovery.db`.
- A UI companion **nunca abre o arquivo**: leituras de config/status/inventário chegam via IPC (Fase C); Memory Notes migram para IPC (`memory:list/create/update/delete` — são poucos registros, latência irrelevante).
- No modo **standalone** (serviço ausente), a UI abre o DB normalmente (comportamento atual preservado) — o `CoreAgent` embarcado é o dono nesse cenário.
- Elimina de vez o risco `SQLITE_BUSY` entre processos (o busy_timeout do plano anterior vira apenas defesa residual do standalone).
- `cache_size` do serviço pode subir (não há mais 2 processos competindo).

| Dado                                                                     | Dono                                           | Acesso da UI companion    |
| ------------------------------------------------------------------------ | ---------------------------------------------- | ------------------------- |
| Outboxes, automation, inventário, consolidação, notificações persistidas | Serviço (DB)                                   | IPC (snapshot/contadores) |
| Cache `agent_configuration_raw`                                          | Serviço (DB)                                   | IPC `config:get`          |
| Memory Notes                                                             | Serviço (DB)                                   | IPC `memory:*` (CRUD)     |
| Cache de UI (chat, preferências de janela)                               | UI (memória ou arquivo próprio em `%APPDATA%`) | local                     |

### Divisão de responsabilidades (mantida do plano anterior, §2.2)

| Componente                                                           | Serviço                            | UI                                 |
| -------------------------------------------------------------------- | ---------------------------------- | ---------------------------------- |
| agentConn, comandos remotos, inventory, automation, P2P, self-update | **dono**                           | não (companion) / sim (standalone) |
| Notificações                                                         | persist + headless + broadcast IPC | renderiza + responde               |
| Chat AI, MCP, remote session, terminal, tray, janela                 | não                                | **dono**                           |

---

## 3. Fases de Execução

### Fase A — Extração do `CoreAgent` (maior risco, ~3-4 dias)

**Objetivo:** eliminar P1, P2, P4, P5.

1. Criar `src/app/coreagent/` com struct `CoreAgent` contendo o que hoje vive no `App` e é do core:
   - Campos: `db`, `agentConn`, `inventorySvc`, `automationSvc`, `syncSvc`, `p2pCoord`, `selfUpdater`, `notificationSvc`, `consolEngine`, `catalogClient`, `debugSvc` (config/credenciais), outboxes, `logs` (ring buffer), `agentConfig`.
   - Métodos: `New(deps)`, `Run(ctx)` (= `runCoreStartup` + `runStagedStartup` **com os delays preservados**: inventory +2s, agentConn +8s, automation/P2P +10s, self-update +12s), `Shutdown()`, `HandleCommand`, `Status()`.
2. Mover a montagem de dependências de `NewApp()` (app.go:233+) referente ao core para `coreagent.New()` — o padrão `Deps{callbacks}` já usado por `notifications.New`, `chat.New`, etc. facilita.
3. `App` (UI) passa a **compor** `*coreagent.CoreAgent` (campo `core`). No standalone, `startup()` chama `a.core.Run(ctx)`; no companion, `startup()` só conecta IPC.
4. Eliminar `runCoreStartup` e a bifurcação `if a.runtimeFlags.ServiceMode` em `startup()`/`shutdown()`.
5. `EmitEvent` vira callback injetado no `CoreAgent` (`EventSink`): UI injeta Wails-emit; serviço injeta `broadcastIPCEvent`; headless injeta no-op/log. Domínios deixam de conhecer Wails.
6. **Guardrails**: `coreagent` não pode importar `github.com/wailsapp/wails/v3/...` — adicionar lint/test de import (ex.: teste que varre `go list -deps` ou analyze de imports).
7. Testes após cada sub-passo: `go test ./...` + `wails build` + smoke do modo serviço.

### Fase B — Entry points separados (~1 dia)

**Objetivo:** eliminar P3 (binário do serviço carregando UI).

1. `src/cmd/discovery-service/main.go` (novo): parse `--service`, chama `svc.Run` com `CoreAgent` — **nunca** importa Wails/frontend embed.
2. `src/main.go` (atual): remove `RunServiceMode`/`IsWindowsServiceProcess`; permanece GUI + `--terminal-dispatcher` + `--remote-session-worker` + `--agent-delete-cleanup`.
3. Mover `servicemode_service_windows.go` → `src/app/coreagent/service_windows.go` (adaptado a `CoreAgent`).
4. Se D1=binário único: manter apenas o roteamento atual, mas com `CoreAgent` já isolado (Fase A entrega 80% do valor).
5. Atualizar `Taskfile.yml` (build dos dois binários) e tags de build.

### Fase C — IPC: completar o contrato companion (~2-3 dias)

**Objetivo:** eliminar P6, P7 (parcial) e viabilizar o DB único (D3).

1. Estender o contrato IPC (JSON-lines atual) com os métodos que os bridges da UI usam no companion, hoje lendo estado local/DB:
   - `inventory:snapshot` (cache do serviço → UI companion, sem abrir SQLite na UI)
   - `config:get/set` (DebugConfig via serviço, incl. cache `agent_configuration_raw`)
   - `logs:tail` (stream do ring buffer)
   - `automation:state` (StateView/TaskView)
   - `updates:pending` — **winget roda no serviço (D2)**: a UI envia `updates:scan` e recebe resultado/progresso via evento IPC; `updates:export` idem. A UI só exibe dados e dispara ações.
   - `memory:list/create/update/delete` — Memory Notes via IPC (DB único, D3)
   - `status:pending_counts` (contadores de outbox para `GetStatusOverview`)
2. Implementar pattern request/response com correlation-id no `IPCClient`/`IPCServer` (hoje só hello/status/event/respond).
3. UI companion: **remover toda abertura do SQLite** (P7) — `startup()` companion não chama `database.Open`; bridges passam a consultar o IPC client. No standalone, abrem normalmente (CoreAgent local).
4. Padronizar o fallback standalone: extrair a máquina de estados companion→standalone (polling 5s, 5min de falhas, `runStagedStartup` tardio) para um `companion.Controller` testável.

### Fase C2 — Notificações nativas + uiOnline (~1 dia)

**Objetivo:** D4 e D5.

1. **Toast nativo do Windows no serviço (D4)**: quando uma notificação `require_confirmation`/informativa chegar e **não houver UI conectada** (IPC `ClientCount()==0`), o serviço dispara toast nativo do Windows (pacote `go-toast` ou Win32 `Shell_NotifyIcon`/`IToastNotificationManager` via syscall — decidir na implementação; `go-toast` é o caminho mais rápido). Comportamento:
   - Toast informativo: dispara e registra persistência (caminho headless atual permanece como fallback).
   - Toast com ação (require_confirmation): botões de ação no toast → resposta volta ao serviço → ciclo NATS. Se o toast não suportar ações na sessão 0 (limitação conhecida de toasts SYSTEM), cai no caminho headless atual (`timeout_policy_applied`).
   - Quando a UI reconectar, o serviço volta a preferir o caminho IPC (toast nativo desativado).
2. **`uiOnline` no heartbeat (D5)**: serviço inclui `uiOnline: bool` (e opcionalmente `uiUsers: n`) no payload de heartbeat do `agentconn`. Servidor pode usar para decidir roteamento de remote session/notificações. Extensão no contrato do servidor (`DiscoveryRMM_API`) — coordenar campo opcional para retrocompatibilidade.

### Fase D — Instalador NSIS + self-update (~1-1,5 dia)

**Objetivo:** eliminar P8.

1. NSIS: instalar `discovery-service.exe` como serviço (`sc create ... start= auto`, failure actions `restart/5000×3`) + manter Scheduled Task da UI.
2. Update: `sc stop DiscoveryService` → **loop `sc query` até STOPPED (timeout 30s)** → replace binário do serviço → `sc start` → update da UI (fluxo atual).
3. Self-update (`core/selfupdate`): o serviço baixa e aplica o update **dos dois artefatos** (service + agent) e se reinicia via SCM; garantia de nunca cair em `ShellExecuteEx("runas")` na sessão 0 (caminho `CreateProcess` breakaway, já existente).
4. Uninstall: `sc stop` + wait + `sc delete` + task existente + ProgramData.

### Fase E — Testes e validação (~1-2 dias)

1. Unitários: contrato IPC (request/response, reconnect), `CoreAgent.Run` staged (delays/guards), `companion.Controller` (fallback após 5min), import-lint Wails no coreagent, Memory Notes via IPC, toast nativo (mock do dispatcher).
2. Checklist manual (do plano anterior, Fase 5):
   - Boot sem login → heartbeat/inventário OK.
   - Login → companion, tray, notificação require_confirmation serviço→UI→resposta.
   - Notificação sem UI conectada → toast nativo do Windows aparece na sessão do usuário (ou headless, conforme tipo).
   - Matar serviço → SCM reinicia em 5s.
   - Self-update com serviço ativo → sem race.
   - Desativar serviço → UI standalone = comportamento atual (DB local aberto pela UI).
   - Inventário SYSTEM vs usuário (delta apps per-user/WMI).
   - Heartbeat com `uiOnline` visível no servidor.
3. Regressão completa da UI (todos os painéis).

---

## 4. Riscos e Mitigações

| Risco                                                                         | Impacto  | Mitigação                                                                                                                             |
| ----------------------------------------------------------------------------- | -------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| Extração `CoreAgent` quebrar UI standalone                                    | Alto     | Passos incrementais + `go test ./...` após cada um; `App` vira wrapper fino; manter bridges Wails intactos                            |
| Drift de contrato IPC entre serviço e UI de versões diferentes durante update | Médio    | Versionar hello (`protocol: 2`); UI cai para standalone se versão incompatível                                                        |
| Winget/PSADT no serviço SYSTEM (sessão 0) divergir do usuário                 | Médio    | Automation de instalações já roda no serviço hoje (Fase 1 do plano anterior) — validar, não mudar                                     |
| Dois binários = dois artefatos no update/release                              | Médio    | Pipeline de build único (Taskfile) + manifest de versão compartilhado (`core/buildinfo`)                                              |
| Fallback 5min sem agente (serviço morto)                                      | Médio    | Já existe; `companion.Controller` extraído permite reduzir/a testar                                                                   |
| Remote session sem usuário logado                                             | Esperado | Por design (exige sessão interativa); fase futura `CreateProcessAsUser` fora do escopo                                                |
| Toast nativo na sessão 0 (SYSTEM) sem ações/banners                           | Médio    | Testar `go-toast` como SYSTEM; se ações não funcionarem, manter caminho headless (`timeout_policy_applied`) como fallback — já existe |
| UI companion sem DB: latência de IPC em listagens grandes                     | Baixo    | Snapshots com paginação/limites; cache local em memória na UI                                                                         |
| Servidor antigo sem suporte a `uiOnline`                                      | Baixo    | Campo opcional no JSON — retrocompatível                                                                                              |

## 5. Pontos em aberto

1. ~~Dois binários vs binário único~~ → **decidido: dois binários** (D1).
2. ~~Winget/updates~~ → **decidido: roda no serviço, UI só exibe** (D2).
3. ~~SQLite~~ → **decidido: DB único, dono = serviço; UI companion via IPC** (D3, §2.3).
4. ~~Toast nativo~~ → **decidido: preparar notificação nativa do Windows quando sem UI** (D4, Fase C2).
5. ~~`uiOnline`~~ → **decidido: incluir no heartbeat** (D5, Fase C2).
6. Biblioteca do toast: `go-toast` vs Win32 puro — decidir na implementação da Fase C2 (recomendo `go-toast` primeiro, com fallback headless garantido).

## 6. Ordem e Estimativa

```
Fase A (CoreAgent)      ~3-4 dias   ← maior risco, entregável independente
Fase B (cmd separado)   ~1 dia
Fase C (IPC completo)   ~2-3 dias
Fase C2 (toast+uiOnline) ~1 dia
Fase D (NSIS/update)    ~1-1,5 dia
Fase E (testes)         ~1-2 dias
                        ─────────
Total                   ~9-12 dias
```

Cada fase termina com `go test ./...` verde + `wails build` validado. Deploy só mediante autorização explícita.
