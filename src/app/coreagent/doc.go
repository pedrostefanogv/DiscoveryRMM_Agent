// Package coreagent formaliza a fronteira core × UI do Discovery Agent
// (PLANO_SEPARACAO_SERVICO_UI.md, Fase A).
//
// Neste pacote ficam os elementos que pertencem ao CORE do agent e são
// compartilhados entre o serviço Windows (cmd/discovery-service) e a UI
// (modo standalone). Nada aqui pode importar Wails — o guardrail é coberto
// por coreagent_import_guardrail_test.go (go list -deps) e pelo teste
// coreagent_guardrail_test.go no package app (ServiceMode nunca inicializa
// app/mainWindow/systemTray).
//
// Estado da extração física:
//
//	Lote 1 (concluído): struct CoreAgent com os campos de tipos vivendo em
//	                    pacotes externos ao package app (DB, AgentConn,
//	                    SyncSvc, AutomationSvc, UpdatesSvc, NotificationSvc,
//	                    MemorySvc, SelfUpdater, DebugSvc, RemoteSessionMgr,
//	                    AgentConfig e afins). O App incorpora o struct via
//	                    embedding — a promoção de campos mantém os acessos
//	                    a.<Campo> existentes; acesso explícito via a.CoreAgent.
//	Lote 2 (concluído): tipos de domínio movidos para cá (coreagent_lote2.go):
//	                    RuntimeFlags, LogBuffer, InventoryCache, ExportConfig,
//	                    AgentInfoCache, AppStorePolicyCache — e os campos
//	                    Logs/InvCache/ExportCfg/AgentInfo/AppStorePolicy/
//	                    P2PCoord/P2PConfig(+mu/seed/ratelimit)/Startup*/
//	                    Activity*/RuntimeFlags promovidos no embed.
//	                    Métodos minúsculos dos wrappers viraram exportados
//	                    (Append/GetAll/Get/Set/Has/Reset/Invalidate/
//	                    EnableFilePersistence/CachePointer).
//	Ficou no App:       packageManagerRouter (depende de *App), mcpRegistry,
//	                    chatSvc, psadtSvc, debugHTTP*, tray, closeMu/
//	                    allowClose, zeroTouch, ipcServer/ipcClient,
//	                    companionStatus, deferredRestart (power-command de UI).
//
// O binário do serviço (cmd/discovery-service) ainda importa o package app
// inteiro (que linka Wails); o comportamento em runtime é correto (Wails
// nunca inicializado). A eliminação total do peso no link exige mover os
// bridges Wails-bound do App (próximo passo natural, fora do escopo do plano).
package coreagent
