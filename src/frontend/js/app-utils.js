"use strict";

var APP_LOCALE_TAGS = {
  "pt-br": "pt-BR",
  "en-us": "en-US",
};

var APP_I18N_DICTIONARY = {
  "pt-br": {
    "app.name": "Discovery",
    "action.collapseMenu": "Colapsar menu",
    "action.maximizeRestore": "Maximizar ou restaurar",
    "action.closeToTray": "Fechar para tray",
    "action.toggleTheme": "Alternar tema",
    "action.refresh": "Atualizar",
    "action.refreshList": "Atualizar Lista",
    "action.refreshStatus": "Atualizar Status",
    "action.reloadCatalog": "Recarregar Catalogo",
    "action.home": "Inicio",
    "action.checkUpdates": "Verificar Atualizacoes",
    "action.checkAgentUpdate": "Verificar Atualizacao do Agente",
    "action.upgradeSelected": "Atualizar Selecionados",
    "action.selectAll": "Selecionar todos",
    "action.previous": "Anterior",
    "action.next": "Proxima",
    "action.close": "Fechar",
    "action.install": "Instalar",
    "action.remove": "Remover",
    "action.send": "Enviar",
    "action.stop": "Stop",
    "action.clear": "Limpar",
    "action.clearChat": "Limpar Chat",
    "action.openFullscreen": "Abrir em tela cheia",
    "action.viewTools": "Ver Ferramentas",
    "action.testConfiguration": "Testar Configuração",
    "action.saveConfiguration": "Salvar Configuração",
    "action.testConnection": "Testar Conexão",
    "action.goToP2P": "Ir para P2P",
    "action.goToPSADT": "Ir para PSADT",
    "action.collectInventory": "Coletar Inventário",
    "action.refreshPolicy": "Atualizar Policy",
    "action.newTicket": "+ Novo Chamado",
    "action.sendTicket": "Enviar Chamado",
    "action.sendComment": "Enviar Comentário",
    "action.closeTicket": "Fechar Chamado",
    "action.back": "Voltar",
    "action.cancel": "Cancelar",
    "action.backToForm": "Voltar ao formulário",
    "action.openP2PDebug": "Abrir P2P Debug",
    "action.p2pRescanPeers": "Pesquisar peers",
    "tab.status": "Status",
    "tab.store": "Loja",
    "tab.updates": "Atualizações",
    "tab.inventory": "Inventário",
    "tab.logs": "Logs",
    "tab.chat": "Chat IA",
    "tab.support": "Suporte",
    "tab.knowledge": "Base de Conhecimento",
    "tab.automation": "Automação",
    "tab.debug": "Debug",
    "tab.psadt": "PSADT",
    "tab.p2p": "P2P",
    "tab.zeroTouchConfig": "Zero-Touch",
    "theme.dark": "Tema Escuro",
    "theme.light": "Tema Claro",
    "topbar.service": "Runtime",
    "topbar.serviceHealthTitle": "Status do Runtime local (tray icon)",
    "window.meta.pc": "PC",
    "window.meta.server": "Servidor",
    "window.meta.connection": "Conexão",
    "common.loading": "Carregando...",
    "common.loadingStatus": "Carregando status...",
    "common.verifying": "Verificando...",
    "common.awaiting": "Aguardando...",
    "common.online": "Online",
    "common.offline": "Offline",
    "common.degraded": "Degradado",
    "common.unavailable": "Indisponível",
    "common.disabled": "Desabilitado",
    "common.idle": "Em espera",
    "common.healthy": "Saudável",
    "common.noAdditionalInfo": "Sem informações adicionais.",
    "common.noOutput": "(sem saída)",
    "common.unknown": "Desconhecido",
    "common.notAvailable": "N/A",
    "common.error": "Erro",
    "common.yes": "Sim",
    "common.no": "Não",
    "pagination.pageOne": "Página 1",
    "pagination.page": "Página {page} de {total}",
    "field.name": "Nome",
    "field.type": "Tipo",
    "field.source": "Origem",
    "field.status": "Status",
    "field.user": "Usuário",
    "field.version": "Versão",
    "field.publisher": "Publisher",
    "field.installId": "ID Instalação",
    "field.serial": "Serial",
    "field.installSource": "Origem Instalação",
    "field.installDate": "Data Instalação",
    "field.origin": "Origem",
    "field.action": "Ação",
    "field.currentVersion": "Versão Atual",
    "field.availableVersion": "Versão Disponível",
    "field.category": "Categoria",
    "field.priority": "Prioridade",
    "field.description": "Descrição",
    "field.rating": "Avaliação",
    "field.process": "Processo",
    "field.protocol": "Protocolo",
    "field.address": "Endereço",
    "field.port": "Porta",
    "field.local": "Local",
    "field.remote": "Remoto",
    "field.id": "ID",
    "field.path": "Path",
    "field.args": "Args",
    "field.family": "Family",
    "field.os": "SO",
    "field.osVersion": "Versão do SO",
    "field.components": "Componentes",
    "field.problems": "Problemas",
    "field.realtime": "Realtime",
    "field.connectedAgents": "Agentes P2P conectados",
    "field.lastInventory": "Último inventário",
    "field.serverPong": "Último ping do servidor",
    "field.nonCriticalTraffic": "Tráfego não crítico",
    "field.nonCriticalUntil": "Adiado até",
    "field.buildDate": "Data do build",
    "field.commit": "Commit",
    "field.lastCheck": "Última verificação",
    "logs.empty": "(sem logs)",
    "logs.emptyOrigin": "(sem logs para a origem selecionada)",
    "logs.cleared": "Logs limpos",
    "logs.clearError": "Erro ao limpar logs: {error}",
    "logs.subtitle": "Terminal de logs",
    "logs.filterByOrigin": "Filtrar logs por origem",
    "logs.origin.all": "Todas",
    "logs.origin.other": "Outros",
    "provisioning.title": "Agente aguardando provisionamento",
    "provisioning.message":
      "Este agente esta aguardando provisionamento pela equipe de TI ou suporte.",
    "provisioning.footnote":
      "Se esta mensagem persistir, entre em contato com a equipe de TI ou suporte.",
    "provisioning.approval.title": "Aguardando aprovação para integração",
    "provisioning.approval.message":
      "Dispositivo provisionado, aguardando aprovação da equipe de TI para integração com o servidor.",
    "provisioning.approval.footnote":
      "A integração será liberada automaticamente após a aprovação. Caso essa mensagem persista, entre em contato com a equipe de TI.",
    "provisioning.completed": "Provisionamento concluído. Interface liberada.",
    "provisioning.refresh": "Verificar novamente",
    "status.summary": "Resumo rápido de saúde do agente",
    "status.connectionTitle": "Status do Agente",
    "status.awaitingRead": "Aguardando leitura do status.",
    "status.applicationTitle": "Aplicação",
    "status.agentUpdateTitle": "Atualização do Agente",
    "status.systemTitle": "Sistema",
    "status.realtimeTitle": "Realtime e Integração",
    "status.localComputer": "Computador local",
    "status.transportState": "Transporte",
    "status.onlineSignal": "Sinal de online",
    "status.failedRead": "Falha na leitura de status",
    "status.couldNotLoadAgentStatus":
      "Não foi possível carregar o status do agente.",
    "status.nonCriticalDeferred": "Adiado",
    "status.nonCriticalDeferredUntil": "Tráfego postergado até {until}",
    "status.nonCriticalNormal": "Normal",
    "status.nonCriticalReason": "Motivo do adiamento",
    "status.checkingUpdate": "Verificando atualização...",
    "status.notCheckedYet": "Nunca verificado",
    "store.searchPlaceholder": "Pesquisar aplicativo por nome, id ou publisher",
    "store.synced": "Loja atualizada por sync{variant}",
    "store.newDataSync": "Novos dados da loja recebidos via sync",
    "store.noPackagesFound": "Nenhum pacote encontrado",
    "store.adjustSearchFilter": "Ajuste o filtro de busca.",
    "store.noDescription": "Sem descrição",
    "store.viewDetails": "Ver detalhes",
    "store.viewDetailsOf": "Ver detalhes de {name}",
    "store.packageId": "ID: {id}",
    "store.catalogLoading": "Carregando catálogo...",
    "store.catalogLoaded": "Catálogo carregado.",
    "store.appsAllowed": "Aplicativos disponíveis: {count}",
    "store.catalogLoadFailure": "Falha ao carregar apps permitidos",
    "store.appMeta": "{publisher} | {version} | ID: {id}",
    "updates.unchecked": "Atualizações não verificadas",
    "updates.checking": "Verificando atualizações pendentes...",
    "updates.clickCheckToList":
      'Clique em "Verificar Atualizações" para listar.',
    "updates.availableCount": "{count} atualização(ões) disponível(is)",
    "updates.foundCount": "{count} atualização(ões) encontrada(s)",
    "updates.nonePending": "Nenhuma atualização pendente",
    "updates.checkError": "Erro ao verificar atualizações",
    "updates.upgrade": "Atualizar",
    "updates.upgradingItem": "Atualizando {id}...",
    "updates.upgradeSuccess": "{id} atualizado com sucesso",
    "updates.upgradeError": "Erro ao atualizar {id}: {error}",
    "updates.batchComplete": "Atualização em lote concluída",
    "inventory.notLoaded": "Inventário não carregado",
    "inventory.collecting": "Coletando dados de inventário...",
    "inventory.initialLoadingText":
      "Buscando cache local e preparando os dados para exibição.",
    "inventory.hardware": "Hardware",
    "inventory.operatingSystem": "Sistema Operacional",
    "inventory.loggedUsers": "Usuários Logados",
    "inventory.volumes": "Volumes (logical_drives)",
    "inventory.networkInterfaces": "Interfaces de Rede",
    "inventory.printers": "Impressoras",
    "inventory.memoryModules": "Memória (Pentes)",
    "inventory.monitors": "Monitores",
    "inventory.gpu": "GPU",
    "inventory.battery": "Bateria",
    "inventory.bitlocker": "BitLocker",
    "inventory.cpuInfo": "CPU Info",
    "inventory.startupItems": "Startup Items",
    "inventory.refreshStartupItems": "Atualizar startup items",
    "inventory.searchStartupPlaceholder":
      "Pesquisar startup item por nome, path, usuário ou origem",
    "inventory.autoexec": "Autoexec",
    "inventory.installedSoftware": "Softwares Instalados",
    "inventory.refreshSoftware": "Atualizar softwares",
    "inventory.searchSoftwarePlaceholder":
      "Pesquisar software por nome, versão, publisher, origem ou data",
    "inventory.networkConnections": "Conexões de Rede",
    "inventory.listeningPorts": "Portas em Escuta",
    "inventory.openConnections": "Conexões Abertas",
    "inventory.refreshConnections": "Atualizar portas e conexões",
    "inventory.refreshListeningPorts": "Atualizar portas em escuta",
    "inventory.searchConnectionsPlaceholder":
      "Pesquisar por processo, porta, endereço...",
    "inventory.noneSoftwareFound": "Nenhum software encontrado.",
    "inventory.noneStartupFound": "Nenhum startup item encontrado.",
    "inventory.noneListeningPortFound": "Nenhuma porta encontrada.",
    "inventory.noneOpenConnectionFound": "Nenhuma conexao encontrada.",
    "inventory.visibleTotal": "Total visivel: {visible}",
    "inventory.visibleInventoryTotal":
      "Total visivel: {visible} | Total inventario: {total}",
    "inventory.collectingNow": "Coletando inventario...",
    "inventory.collectedAt": "Coletado em {date}",
    "inventory.updated": "Inventário atualizado.",
    "inventory.collectFailed": "Falha ao coletar inventário",
    "inventory.initialCollectFailed":
      "Falha ao coletar os dados iniciais. Clique em Coletar Inventário para tentar novamente.",
    "inventory.exportPdfRunningFeedback": "Exportando inventário em PDF...",
    "inventory.exportPdfRunningStatus": "Exportação PDF em andamento...",
    "inventory.exportNoFile": "Nenhum arquivo retornado do servidor",
    "inventory.exportNoPath": "Falha: nenhum caminho retornado",
    "inventory.exportPdfSuccessFeedback": "PDF exportado: {path}",
    "inventory.exportPdfSuccessStatus": "PDF criado com sucesso em: {path}",
    "inventory.exportPdfError": "Falha ao exportar PDF: {error}",
    "inventory.osqueryInstalled": "osquery: instalado ({path})",
    "inventory.osqueryNotDetected":
      "osquery: não detectado (pacote: {packageId})",
    "inventory.osqueryCheckError": "osquery: erro ao verificar ({error})",
    "inventory.installingOsquery": "Instalando osquery via winget...",
    "inventory.osqueryInstalledFeedback": "Instalação do osquery concluída.",
    "support.title": "Suporte",
    "support.subtitle": "Chamados vinculados a este agente",
    "support.myTickets": "Meus Chamados",
    "support.noTickets": "Nenhum chamado.",
    "support.closeTicketTitle": "Fechar Chamado",
    "support.noRating": "Sem avaliação",
    "support.finalStateOptional": "Estado final (opcional)",
    "support.closeWithDefaultState": "Fechar com estado padrão",
    "support.customFinalStateGuidPlaceholder":
      "GUID do estado final customizado",
    "support.closingCommentOptional": "Comentário de fechamento (opcional)",
    "support.describeAppliedSolution": "Descreva a solução aplicada...",
    "support.comments": "Comentários",
    "support.writeCommentPlaceholder": "Escreva um comentário...",
    "support.openTicketTitle": "Abrir Chamado",
    "support.ticketTitle": "Título *",
    "support.problemSummary": "Resumo do problema",
    "support.select": "Selecione...",
    "support.category.hardware": "Hardware",
    "support.category.software": "Software",
    "support.category.network": "Rede",
    "support.category.access": "Acesso / Permissões",
    "support.category.email": "E-mail",
    "support.category.printer": "Impressora",
    "support.category.vpn": "VPN",
    "support.category.other": "Outro",
    "support.priority.low": "Baixa",
    "support.priority.medium": "Média",
    "support.priority.high": "Alta",
    "support.priority.critical": "Crítica",
    "support.descriptionRequired": "Descrição *",
    "support.describeProblemDetails": "Descreva o problema com detalhes...",
    "support.fillTitleDescription": "Preencha título e descrição",
    "support.sending": "Enviando...",
    "support.submittingTicket": "Enviando chamado...",
    "support.ticketCreatedSuccess": "Chamado criado com sucesso!",
    "support.ticketCreateError": "Erro ao criar chamado: {error}",
    "support.ticketListLoadError": "Erro ao carregar chamados: {error}",
    "support.enterComment": "Digite um comentario",
    "support.commentSent": "Comentario enviado",
    "support.commentSendError": "Erro ao enviar comentario: {error}",
    "support.invalidRating": "Avaliação inválida. Informe de 0 a 5.",
    "support.closing": "Fechando...",
    "support.ticketClosedSuccess": "Chamado fechado com sucesso",
    "support.ticketCloseError": "Erro ao fechar chamado: {error}",
    "support.agentLabel": "Agente: {name}",
    "support.serverNotConfigured":
      "Servidor não configurado. Configure em Debug.",
    "support.noTicketsPrompt":
      'Nenhum chamado no momento. Clique em "+ Novo Chamado" para abrir um.',
    "support.defaultOpenStatus": "Aberto",
    "support.ticketOpenedAt": "Aberto em: {date}",
    "support.ratingDisplay": "Avaliacao: {rating}",
    "support.ratedAt": "Avaliado em: {date}",
    "support.ratedBy": "Avaliado por: {name}",
    "support.loadingComments": "Carregando comentários...",
    "support.noComments": "Nenhum comentário.",
    "support.commentLoadError": "Erro ao carregar comentários: {error}",
    "support.user": "Usuário",
    "support.internal": "Interno",
    "knowledge.title": "Base de Conhecimento",
    "knowledge.subtitle": "Artigos de manutenção e troubleshooting de PCs",
    "knowledge.refresh": "Atualizar base de conhecimento",
    "knowledge.searchPlaceholder":
      "Buscar por título, categoria, tag ou conteúdo",
    "knowledge.loadingArticles": "Carregando artigos...",
    "knowledge.article": "Artigo",
    "knowledge.noArticlesFound": "Nenhum artigo encontrado.",
    "knowledge.loadError": "Erro ao carregar base de conhecimento.",
    "automation.title": "Automação",
    "automation.subtitle": "Policy sync local e tarefas aplicadas ao agent",
    "automation.includeScriptContent":
      "Incluir conteúdo de script no sync manual",
    "automation.notes": "Observações",
    "automation.noSyncYet": "Nenhum sync executado ainda.",
    "automation.localExecution": "Execução Local",
    "automation.pendingCallbacks": "{count} callbacks pendentes",
    "automation.ackResultMessage":
      "ACK e RESULT são enviados imediatamente quando possível e entram em fila persistente quando o backend estiver indisponível.",
    "automation.resolvedTasks": "Tarefas Resolvidas",
    "automation.zeroTasks": "{count} tarefas",
    "automation.noPolicyLoaded": "Nenhuma policy carregada.",
    "automation.recentExecutions": "Execuções Recentes",
    "automation.zeroExecutions": "{count} execuções",
    "automation.noExecutions": "Nenhuma execução registrada.",
    "automation.awaitingConfig": "Aguardando configuração",
    "automation.synchronized": "Sincronizado",
    "automation.noCommunication": "Sem comunicacao",
    "automation.noChanges": "Sem alteracoes",
    "automation.newOrLocalPolicy": "Policy nova ou local",
    "automation.connection": "Conexao",
    "automation.tasks": "Tasks",
    "automation.pendingCallbacksLabel": "Callbacks pendentes",
    "automation.upToDate": "UpToDate",
    "automation.lastSync": "Último sync",
    "automation.lastAttempt": "Última tentativa",
    "automation.localCache": "Cache local",
    "automation.generatedAt": "Policy gerada em {date}",
    "automation.manualSyncWithScript":
      "Sync manual com conteúdo de script habilitado",
    "automation.lastErrorLine": "Ultimo erro: {error}",
    "automation.noAlerts": "Sem alertas no momento.",
    "automation.noneResolvedTasks": "Nenhuma tarefa resolvida para o agent.",
    "automation.action": "Ação",
    "automation.scope": "Escopo",
    "automation.updatedAt": "Atualizado em",
    "automation.untitledTask": "Tarefa sem nome",
    "automation.viewLogs": "Ver logs",
    "automation.hideLogs": "Ocultar logs",
    "automation.pendingCallbackChip": "Callback pendente",
    "automation.executionTitle": "Execução",
    "automation.summary": "Resumo",
    "automation.errorLabel": "Erro",
    "automation.start": "Início",
    "automation.end": "Fim",
    "automation.inProgress": "Em andamento",
    "automation.duration": "Duração",
    "automation.loadingState": "Carregando estado da automação...",
    "automation.waitingServerConfig":
      "Automação aguardando configuração de servidor/token.",
    "automation.lastErrorState": "Automacao com ultimo erro registrado.",
    "automation.policyLoadedSuccess": "Policy carregada com sucesso.",
    "automation.policyLoadedLocal":
      "Policy local carregada sem comunicação atual com o backend.",
    "automation.loadFailed": "Falha ao carregar automação: {error}",
    "automation.syncingPolicy": "Sincronizando policy...",
    "automation.syncWarning": "Policy sincronizada com alerta.",
    "automation.syncSuccess": "Policy sincronizada com sucesso.",
    "automation.syncFailed": "Falha no policy sync: {error}",
    "debug.subtitle": "Debug - configuração de conexão com servidor remoto",
    "debug.agentStatusTitle": "Status do Agente",
    "debug.connectionSettings": "Configurações de Conexão",
    "debug.apiServer": "Servidor API (HTTP/HTTPS)",
    "debug.authToken": "Token de Autenticação (API)",
    "debug.natsServer": "Servidor NATS (Comandos)",
    "debug.agentId": "Agent ID (NATS)",
    "debug.p2pFirstInstall": "Automação: instalar via P2P-first (teste)",
    "debug.p2pFalseOption": "false (usar Winget CLI)",
    "debug.p2pTrueOption": "true (tentar P2P e fallback Winget)",
    "debug.testResponse": "Resposta do teste de conexão",
    "debug.offlineDisconnected": "Offline / Desconectado",
    "debug.serverLabel": "servidor: {server}",
    "debug.statusLoadError": "Erro ao carregar status: {error}",
    "debug.saving": "Salvando...",
    "debug.savedSuccess": "Configuração salva com sucesso.",
    "debug.saveError": "Erro ao salvar: {error}",
    "debug.testingConnection": "Testando conexão...",
    "debug.connectedSuccess": "Conectado com sucesso.",
    "debug.connectionFailure": "Falha na conexão: {error}",
    "chat.subtitle":
      "Chat IA - converse com a IA para gerenciar seu computador",
    "chat.memories": "Memórias",
    "chat.apiServerOptional": "Servidor API (opcional)",
    "chat.agentTokenOptional": "Token do Agente (opcional)",
    "chat.modelCompatibility": "Modelo (compatibilidade)",
    "chat.maxTokensOptional": "Max Tokens (opcional)",
    "chat.assistantInstructionsOptional": "Instruções do Assistente (Opcional)",
    "chat.systemPromptPlaceholder":
      "Ex.: Responda com foco em inventário corporativo e sempre cite riscos antes de executar ações.",
    "chat.configUsesDebugFallback":
      "Se endpoint/token ficarem vazios, o chat usa automaticamente apiScheme/apiServer/authToken da aba Debug.",
    "chat.inputPlaceholder":
      "Digite sua mensagem... (Enter para enviar, Shift+Enter para nova linha)",
    "chat.logsTitle": "Logs do Chat IA",
    "chat.localMemories": "Memórias Locais",
    "chat.noMemoryFound": "Nenhuma memória encontrada.",
    "chat.stopping": "Parando...",
    "chat.thinking": "Pensando...",
    "chat.responseInterrupted": "_Resposta interrompida pelo usuário._",
    "chat.errorUnknown": "Erro: {error}",
    "chat.maxTokensValidation": "Max Tokens deve ser 0 ou maior",
    "chat.configSavedSuccess": "Configuração de IA salva com sucesso",
    "chat.configSaveError": "Erro ao salvar configuração: {error}",
    "chat.configTesting": "Testando configuração de IA...",
    "chat.configTestSuccess": "Teste concluído com sucesso{suffix}",
    "chat.configTestFailure": "Falha no teste da configuração: {error}",
    "chat.toolsLoadError": "Erro ao carregar ferramentas",
    "chat.noLogsYet": "(sem logs de chat ainda)",
    "chat.logsLoadError": "Erro ao carregar logs: {error}",
    "chat.updatedAt": "(atualizado: {date})",
    "chat.memoriesLoadError": "Erro ao carregar memorias: {error}",
    "chat.memoryDeleteError": "Erro ao excluir memorias: {error}",
    "chat.cleared": "Chat limpo",
    "chat.clearError": "Erro: {error}",
    "action.delete": "Excluir",
    "p2p.nativeSubtitle":
      "P2P nativo: descoberta, distribuicao hibrida e cache temporario",
    "p2p.status": "Status",
    "p2p.transfers": "Transferencias em Andamento",
    "p2p.openFolder": "Abrir pasta de downloads P2P",
    "p2p.artifactsLocal": "Artifacts Locais",
    "p2p.peersDiscovered": "Peers Descobertos",
    "p2p.configuration": "Configuracao P2P",
    "p2p.audit": "Auditoria",
    "p2p.selectFileToPublish": "Selecionar Arquivo para Publicar",
    "p2p.deleteAll": "Apagar Todos",
    "p2p.artifact": "Artifact",
    "p2p.size": "Tamanho",
    "p2p.checksum": "Checksum",
    "p2p.agent": "Agent",
    "p2p.address": "Endereco",
    "p2p.sourceSlash": "Origem",
    "p2p.actions": "Acoes",
    "p2p.viewArtifacts": "Ver artifacts deste peer",
    "p2p.noPeers": "Nenhum peer descoberto.",
    "p2p.noArtifacts": "Nenhum artifact local.",
    "p2p.noAudit": "Nenhuma atividade registrada.",
    "p2p.artifactsAvailable": "Artifacts disponiveis neste peer",
    "p2p.pull": "Puxar",
    "p2p.save": "Salvar",
    "p2p.refresh": "Atualizar",
    "p2p.cleanCache": "Limpar Cache",
    "p2p.cleanAll": "Limpar Tudo",
    "p2p.scanPeers": "Pesquisar peers na rede novamente",
    "p2p.active": "Ativo",
    "p2p.discovery": "Discovery",
    "p2p.ttl": "TTL (horas)",
    "p2p.seedPercent": "Seed %",
    "p2p.minSeeds": "Min seeds",
    "p2p.tokenMinutes": "Token (min)",
    "p2p.sharedSecret": "Shared Secret (opcional)",
    "p2p.sharedSecretHint": "habilita autenticacao do endpoint /p2p/replicate",
    "p2p.auditAllActions": "Todas as acoes",
    "p2p.auditAllPeers": "Todos os peers",
    "p2p.auditAllStatus": "Todos os status",
    "p2p.auditSuccess": "sucesso",
    "p2p.auditError": "erro",
    "p2p.loadingData": "Carregando dados P2P...",
    "p2p.loadError": "Falha ao carregar dados P2P: {error}",
    "psadt.title": "PSADT Debug Window",
    "psadt.subtitle":
      "Diagnostico real de notificacoes, modulo e execucao PSAppDeployToolkit.",
    "psadt.refreshState": "Atualizar Estado",
    "psadt.currentState": "Estado Atual",
    "psadt.moduleTitle": "Modulo PSADT",
    "psadt.desiredVersion": "Versao desejada",
    "psadt.source": "Fonte",
    "psadt.checkModule": "Verificar Modulo",
    "psadt.installModule": "Instalar Modulo",
    "psadt.themePreview": "Tema de Preview",
    "psadt.notificationTitle": "Titulo",
    "psadt.message": "Mensagem",
    "psadt.severity": "Gravidade",
    "psadt.mode": "Modo",
    "psadt.layout": "Layout",
    "psadt.emitNotification": "Emitir Notificacao",
    "psadt.close": "Fechar",
    "psadt.previewHistory": "Historico de Preview",
    "ztc.subtitle":
      "Zero-Touch: agentes sem credenciais sao provisionados via rede P2P",
    "ztc.description":
      "Agentes instalados sem credenciais pre-definidas, aguardam provisionamento via rede P2P. O agente que possui credenciais pre-definidas atua como 'Provisioner' e distribui as credenciais para os agentes sem credenciais. O Provisioner deve estar online e conectado ao servidor para que o provisionamento ocorra. Os novos agentes provisionados recebem as credenciais e devem ser autorizados no servidor para que possam se conectar e operar normalmente.",
    "ztc.recentEvents": "Eventos recentes de provisionamento",
    "ztc.noEvents": "Nenhum evento de provisionamento Zero-Touch registrado.",
    "ztc.loading": "Carregando dados de provisionamento Zero-Touch...",
  },
  "en-us": {
    "app.name": "Discovery",
    "action.collapseMenu": "Collapse menu",
    "action.maximizeRestore": "Maximize or restore",
    "action.closeToTray": "Close to tray",
    "action.toggleTheme": "Toggle theme",
    "action.refresh": "Refresh",
    "action.refreshList": "Refresh List",
    "action.refreshStatus": "Refresh Status",
    "action.reloadCatalog": "Reload Catalog",
    "action.home": "Home",
    "action.checkUpdates": "Check Updates",
    "action.checkAgentUpdate": "Check Agent Update",
    "action.upgradeSelected": "Upgrade Selected",
    "action.selectAll": "Select all",
    "action.previous": "Previous",
    "action.next": "Next",
    "action.close": "Close",
    "action.install": "Install",
    "action.remove": "Remove",
    "action.send": "Send",
    "action.stop": "Stop",
    "action.clear": "Clear",
    "action.clearChat": "Clear Chat",
    "action.openFullscreen": "Open full screen",
    "action.viewTools": "View Tools",
    "action.testConfiguration": "Test Configuration",
    "action.saveConfiguration": "Save Configuration",
    "action.testConnection": "Test Connection",
    "action.goToP2P": "Go to P2P",
    "action.goToPSADT": "Go to PSADT",
    "action.collectInventory": "Collect Inventory",
    "action.refreshPolicy": "Refresh Policy",
    "action.newTicket": "+ New Ticket",
    "action.sendTicket": "Submit Ticket",
    "action.sendComment": "Send Comment",
    "action.closeTicket": "Close Ticket",
    "action.back": "Back",
    "action.backToForm": "Back to form",
    "action.openP2PDebug": "Open P2P Debug",
    "action.p2pRescanPeers": "Scan peers",
    "tab.status": "Status",
    "tab.store": "Store",
    "tab.updates": "Updates",
    "tab.inventory": "Inventory",
    "tab.logs": "Logs",
    "tab.chat": "AI Chat",
    "tab.support": "Support",
    "tab.knowledge": "Knowledge Base",
    "tab.automation": "Automation",
    "tab.debug": "Debug",
    "tab.psadt": "PSADT",
    "tab.p2p": "P2P",
    "tab.zeroTouchConfig": "Zero-Touch",
    "theme.dark": "Dark theme",
    "theme.light": "Light theme",
    "topbar.service": "Runtime",
    "topbar.serviceHealthTitle": "Local runtime status (tray icon)",
    "window.meta.pc": "PC",
    "window.meta.server": "Server",
    "window.meta.connection": "Connection",
    "common.loading": "Loading...",
    "common.loadingStatus": "Loading status...",
    "common.verifying": "Checking...",
    "common.awaiting": "Waiting...",
    "common.online": "Online",
    "common.offline": "Offline",
    "common.degraded": "Degraded",
    "common.unavailable": "Unavailable",
    "common.disabled": "Disabled",
    "common.idle": "Idle",
    "common.healthy": "Healthy",
    "common.noAdditionalInfo": "No additional information.",
    "common.noOutput": "(no output)",
    "common.unknown": "Unknown",
    "common.notAvailable": "N/A",
    "common.error": "Error",
    "common.yes": "Yes",
    "common.no": "No",
    "pagination.pageOne": "Page 1",
    "pagination.page": "Page {page} of {total}",
    "field.name": "Name",
    "field.type": "Type",
    "field.source": "Source",
    "field.status": "Status",
    "field.user": "User",
    "field.version": "Version",
    "field.publisher": "Publisher",
    "field.installId": "Install ID",
    "field.serial": "Serial",
    "field.installSource": "Install Source",
    "field.installDate": "Install Date",
    "field.origin": "Origin",
    "field.action": "Action",
    "field.currentVersion": "Current Version",
    "field.availableVersion": "Available Version",
    "field.category": "Category",
    "field.priority": "Priority",
    "field.description": "Description",
    "field.rating": "Rating",
    "field.process": "Process",
    "field.protocol": "Protocol",
    "field.address": "Address",
    "field.port": "Port",
    "field.local": "Local",
    "field.remote": "Remote",
    "field.id": "ID",
    "field.path": "Path",
    "field.args": "Args",
    "field.family": "Family",
    "field.os": "OS",
    "field.osVersion": "OS Version",
    "field.components": "Components",
    "field.problems": "Problems",
    "field.realtime": "Realtime",
    "field.connectedAgents": "Connected P2P Agents",
    "field.lastInventory": "Last Inventory",
    "field.serverPong": "Last server pong",
    "field.nonCriticalTraffic": "Non-critical traffic",
    "field.nonCriticalUntil": "Deferred until",
    "field.buildDate": "Build date",
    "field.commit": "Commit",
    "field.lastCheck": "Last Check",
    "logs.empty": "(no logs)",
    "logs.emptyOrigin": "(no logs for the selected source)",
    "logs.cleared": "Logs cleared",
    "logs.clearError": "Failed to clear logs: {error}",
    "logs.subtitle": "Log terminal",
    "logs.filterByOrigin": "Filter logs by origin",
    "logs.origin.all": "All",
    "logs.origin.other": "Other",
    "provisioning.title": "Agent awaiting provisioning",
    "provisioning.message":
      "This agent is waiting for provisioning by the IT or support team.",
    "provisioning.footnote":
      "If this message persists, contact the IT or support team.",
    "provisioning.approval.title": "Awaiting approval for integration",
    "provisioning.approval.message":
      "Device provisioned, awaiting IT approval to integrate with the server.",
    "provisioning.approval.footnote":
      "Integration will be enabled automatically after approval. If this message persists, contact the IT team.",
    "provisioning.completed": "Provisioning completed. Interface unlocked.",
    "provisioning.refresh": "Check again",
    "status.summary": "Quick agent health summary",
    "status.connectionTitle": "Agent Status",
    "status.awaitingRead": "Waiting for status read.",
    "status.applicationTitle": "Application",
    "status.agentUpdateTitle": "Agent Update",
    "status.systemTitle": "System",
    "status.realtimeTitle": "Realtime and Integration",
    "status.localComputer": "Local computer",
    "status.transportState": "Transport",
    "status.onlineSignal": "Online signal",
    "status.failedRead": "Failed to read status",
    "status.couldNotLoadAgentStatus": "Could not load agent status.",
    "status.nonCriticalDeferred": "Deferred",
    "status.nonCriticalDeferredUntil": "Traffic deferred until {until}",
    "status.nonCriticalNormal": "Normal",
    "status.nonCriticalReason": "Deferred reason",
    "status.checkingUpdate": "Checking for updates...",
    "status.notCheckedYet": "Never checked",
    "store.searchPlaceholder": "Search app by name, id or publisher",
    "store.synced": "Store updated by sync{variant}",
    "store.newDataSync": "New store data received via sync",
    "store.noPackagesFound": "No packages found",
    "store.adjustSearchFilter": "Adjust the search filter.",
    "store.noDescription": "No description",
    "store.viewDetails": "View details",
    "store.viewDetailsOf": "View details for {name}",
    "store.packageId": "ID: {id}",
    "store.catalogLoading": "Loading catalog...",
    "store.catalogLoaded": "Catalog loaded.",
    "store.appsAllowed": "Available apps: {count}",
    "store.catalogLoadFailure": "Failed to load allowed apps",
    "store.appMeta": "{publisher} | {version} | ID: {id}",
    "updates.unchecked": "Updates not checked",
    "updates.checking": "Checking pending updates...",
    "updates.clickCheckToList": 'Click "Check Updates" to list them.',
    "updates.availableCount": "{count} update(s) available",
    "updates.foundCount": "{count} update(s) found",
    "updates.nonePending": "No pending updates",
    "updates.checkError": "Failed to check updates",
    "updates.upgrade": "Upgrade",
    "updates.upgradingItem": "Upgrading {id}...",
    "updates.upgradeSuccess": "{id} upgraded successfully",
    "updates.upgradeError": "Failed to upgrade {id}: {error}",
    "updates.batchComplete": "Batch upgrade completed",
    "inventory.notLoaded": "Inventory not loaded",
    "inventory.collecting": "Collecting inventory data...",
    "inventory.initialLoadingText":
      "Fetching local cache and preparing data for display.",
    "inventory.hardware": "Hardware",
    "inventory.operatingSystem": "Operating System",
    "inventory.loggedUsers": "Logged Users",
    "inventory.volumes": "Volumes (logical_drives)",
    "inventory.networkInterfaces": "Network Interfaces",
    "inventory.printers": "Printers",
    "inventory.memoryModules": "Memory (Modules)",
    "inventory.monitors": "Monitors",
    "inventory.gpu": "GPU",
    "inventory.battery": "Battery",
    "inventory.bitlocker": "BitLocker",
    "inventory.cpuInfo": "CPU Info",
    "inventory.startupItems": "Startup Items",
    "inventory.refreshStartupItems": "Refresh startup items",
    "inventory.searchStartupPlaceholder":
      "Search startup item by name, path, user or source",
    "inventory.autoexec": "Autoexec",
    "inventory.installedSoftware": "Installed Software",
    "inventory.refreshSoftware": "Refresh software",
    "inventory.searchSoftwarePlaceholder":
      "Search software by name, version, publisher, source or date",
    "inventory.networkConnections": "Network Connections",
    "inventory.listeningPorts": "Listening Ports",
    "inventory.openConnections": "Open Connections",
    "inventory.refreshConnections": "Refresh ports and connections",
    "inventory.refreshListeningPorts": "Refresh listening ports",
    "inventory.searchConnectionsPlaceholder":
      "Search by process, port, address...",
    "inventory.noneSoftwareFound": "No software found.",
    "inventory.noneStartupFound": "No startup item found.",
    "inventory.noneListeningPortFound": "No port found.",
    "inventory.noneOpenConnectionFound": "No connection found.",
    "inventory.visibleTotal": "Visible total: {visible}",
    "inventory.visibleInventoryTotal":
      "Visible total: {visible} | Inventory total: {total}",
    "inventory.collectingNow": "Collecting inventory...",
    "inventory.collectedAt": "Collected at {date}",
    "inventory.updated": "Inventory updated.",
    "inventory.collectFailed": "Failed to collect inventory",
    "inventory.initialCollectFailed":
      "Failed to collect the initial data. Click Collect Inventory to try again.",
    "inventory.exportPdfRunningFeedback": "Exporting inventory to PDF...",
    "inventory.exportPdfRunningStatus": "PDF export in progress...",
    "inventory.exportNoFile": "No file returned from the server",
    "inventory.exportNoPath": "Failed: no path returned",
    "inventory.exportPdfSuccessFeedback": "PDF exported: {path}",
    "inventory.exportPdfSuccessStatus": "PDF created successfully at: {path}",
    "inventory.exportPdfError": "Failed to export PDF: {error}",
    "inventory.osqueryInstalled": "osquery: installed ({path})",
    "inventory.osqueryNotDetected":
      "osquery: not detected (package: {packageId})",
    "inventory.osqueryCheckError": "osquery: error while checking ({error})",
    "inventory.installingOsquery": "Installing osquery via winget...",
    "inventory.osqueryInstalledFeedback": "osquery installation completed.",
    "support.title": "Support",
    "support.subtitle": "Tickets linked to this agent",
    "support.myTickets": "My Tickets",
    "support.noTickets": "No tickets.",
    "support.closeTicketTitle": "Close Ticket",
    "support.noRating": "No rating",
    "support.finalStateOptional": "Final state (optional)",
    "support.closeWithDefaultState": "Close with default state",
    "support.customFinalStateGuidPlaceholder": "GUID of custom final state",
    "support.closingCommentOptional": "Closing comment (optional)",
    "support.describeAppliedSolution": "Describe the applied solution...",
    "support.comments": "Comments",
    "support.writeCommentPlaceholder": "Write a comment...",
    "support.openTicketTitle": "Open Ticket",
    "support.ticketTitle": "Title *",
    "support.problemSummary": "Problem summary",
    "support.select": "Select...",
    "support.category.hardware": "Hardware",
    "support.category.software": "Software",
    "support.category.network": "Network",
    "support.category.access": "Access / Permissions",
    "support.category.email": "Email",
    "support.category.printer": "Printer",
    "support.category.vpn": "VPN",
    "support.category.other": "Other",
    "support.priority.low": "Low",
    "support.priority.medium": "Medium",
    "support.priority.high": "High",
    "support.priority.critical": "Critical",
    "support.descriptionRequired": "Description *",
    "support.describeProblemDetails": "Describe the problem in detail...",
    "support.fillTitleDescription": "Fill in title and description",
    "support.sending": "Sending...",
    "support.submittingTicket": "Submitting ticket...",
    "support.ticketCreatedSuccess": "Ticket created successfully!",
    "support.ticketCreateError": "Failed to create ticket: {error}",
    "support.ticketListLoadError": "Failed to load tickets: {error}",
    "support.enterComment": "Enter a comment",
    "support.commentSent": "Comment sent",
    "support.commentSendError": "Failed to send comment: {error}",
    "support.invalidRating": "Invalid rating. Provide a value from 0 to 5.",
    "support.closing": "Closing...",
    "support.ticketClosedSuccess": "Ticket closed successfully",
    "support.ticketCloseError": "Failed to close ticket: {error}",
    "support.agentLabel": "Agent: {name}",
    "support.serverNotConfigured":
      "Server not configured. Configure it in Debug.",
    "support.noTicketsPrompt":
      'No tickets at the moment. Click "+ New Ticket" to open one.',
    "support.defaultOpenStatus": "Open",
    "support.ticketOpenedAt": "Opened at: {date}",
    "support.ratingDisplay": "Rating: {rating}",
    "support.ratedAt": "Rated at: {date}",
    "support.ratedBy": "Rated by: {name}",
    "support.loadingComments": "Loading comments...",
    "support.noComments": "No comments.",
    "support.commentLoadError": "Failed to load comments: {error}",
    "support.user": "User",
    "support.internal": "Internal",
    "knowledge.title": "Knowledge Base",
    "knowledge.subtitle": "PC maintenance and troubleshooting articles",
    "knowledge.refresh": "Refresh knowledge base",
    "knowledge.searchPlaceholder": "Search by title, category, tag or content",
    "knowledge.loadingArticles": "Loading articles...",
    "knowledge.article": "Article",
    "knowledge.noArticlesFound": "No articles found.",
    "knowledge.loadError": "Failed to load knowledge base.",
    "automation.title": "Automation",
    "automation.subtitle": "Local policy sync and tasks applied to the agent",
    "automation.includeScriptContent": "Include script content in manual sync",
    "automation.notes": "Notes",
    "automation.noSyncYet": "No sync executed yet.",
    "automation.localExecution": "Local Execution",
    "automation.pendingCallbacks": "{count} pending callbacks",
    "automation.ackResultMessage":
      "ACK and RESULT are sent immediately when possible and enter a persistent queue when the backend is unavailable.",
    "automation.resolvedTasks": "Resolved Tasks",
    "automation.zeroTasks": "{count} tasks",
    "automation.noPolicyLoaded": "No policy loaded.",
    "automation.recentExecutions": "Recent Executions",
    "automation.zeroExecutions": "{count} executions",
    "automation.noExecutions": "No executions recorded.",
    "automation.awaitingConfig": "Waiting for configuration",
    "automation.synchronized": "Synchronized",
    "automation.noCommunication": "No communication",
    "automation.noChanges": "No changes",
    "automation.newOrLocalPolicy": "New or local policy",
    "automation.connection": "Connection",
    "automation.tasks": "Tasks",
    "automation.pendingCallbacksLabel": "Pending callbacks",
    "automation.upToDate": "UpToDate",
    "automation.lastSync": "Last sync",
    "automation.lastAttempt": "Last attempt",
    "automation.localCache": "Local cache",
    "automation.generatedAt": "Policy generated at {date}",
    "automation.manualSyncWithScript":
      "Manual sync with script content enabled",
    "automation.lastErrorLine": "Last error: {error}",
    "automation.noAlerts": "No alerts at the moment.",
    "automation.noneResolvedTasks": "No tasks resolved for the agent.",
    "automation.action": "Action",
    "automation.scope": "Scope",
    "automation.updatedAt": "Updated at",
    "automation.untitledTask": "Untitled task",
    "automation.viewLogs": "View logs",
    "automation.hideLogs": "Hide logs",
    "automation.pendingCallbackChip": "Pending callback",
    "automation.executionTitle": "Execution",
    "automation.summary": "Summary",
    "automation.errorLabel": "Error",
    "automation.start": "Start",
    "automation.end": "End",
    "automation.inProgress": "In progress",
    "automation.duration": "Duration",
    "automation.loadingState": "Loading automation state...",
    "automation.waitingServerConfig":
      "Automation is waiting for server/token configuration.",
    "automation.lastErrorState": "Automation has a last recorded error.",
    "automation.policyLoadedSuccess": "Policy loaded successfully.",
    "automation.policyLoadedLocal":
      "Local policy loaded with no current backend communication.",
    "automation.loadFailed": "Failed to load automation: {error}",
    "automation.syncingPolicy": "Syncing policy...",
    "automation.syncWarning": "Policy synced with warning.",
    "automation.syncSuccess": "Policy synced successfully.",
    "automation.syncFailed": "Policy sync failed: {error}",
    "debug.subtitle": "Debug - remote server connection settings",
    "debug.agentStatusTitle": "Agent Status",
    "debug.connectionSettings": "Connection Settings",
    "debug.apiServer": "API Server (HTTP/HTTPS)",
    "debug.authToken": "Authentication Token (API)",
    "debug.natsServer": "NATS Server (Commands)",
    "debug.agentId": "Agent ID (NATS)",
    "debug.p2pFirstInstall": "Automation: install via P2P-first (test)",
    "debug.p2pFalseOption": "false (use Winget CLI)",
    "debug.p2pTrueOption": "true (try P2P and fallback to Winget)",
    "debug.testResponse": "Connection test response",
    "debug.offlineDisconnected": "Offline / Disconnected",
    "debug.serverLabel": "server: {server}",
    "debug.statusLoadError": "Failed to load status: {error}",
    "debug.saving": "Saving...",
    "debug.savedSuccess": "Configuration saved successfully.",
    "debug.saveError": "Failed to save: {error}",
    "debug.testingConnection": "Testing connection...",
    "debug.connectedSuccess": "Connected successfully.",
    "debug.connectionFailure": "Connection failed: {error}",
    "chat.subtitle": "AI Chat - talk to the AI to manage your computer",
    "chat.memories": "Memories",
    "chat.apiServerOptional": "API Server (optional)",
    "chat.agentTokenOptional": "Agent Token (optional)",
    "chat.modelCompatibility": "Model (compatibility)",
    "chat.maxTokensOptional": "Max Tokens (optional)",
    "chat.assistantInstructionsOptional": "Assistant Instructions (Optional)",
    "chat.systemPromptPlaceholder":
      "Example: Answer with a focus on corporate inventory and always cite risks before executing actions.",
    "chat.configUsesDebugFallback":
      "If endpoint/token are empty, chat automatically uses apiScheme/apiServer/authToken from the Debug tab.",
    "chat.inputPlaceholder":
      "Type your message... (Enter to send, Shift+Enter for a new line)",
    "chat.logsTitle": "AI Chat Logs",
    "chat.localMemories": "Local Memories",
    "chat.noMemoryFound": "No memory found.",
    "chat.stopping": "Stopping...",
    "chat.thinking": "Thinking...",
    "chat.responseInterrupted": "_Response interrupted by the user._",
    "chat.errorUnknown": "Error: {error}",
    "chat.maxTokensValidation": "Max Tokens must be 0 or greater",
    "chat.configSavedSuccess": "AI configuration saved successfully",
    "chat.configSaveError": "Failed to save configuration: {error}",
    "chat.configTesting": "Testing AI configuration...",
    "chat.configTestSuccess": "Test completed successfully{suffix}",
    "chat.configTestFailure": "Configuration test failed: {error}",
    "chat.toolsLoadError": "Failed to load tools",
    "chat.noLogsYet": "(no chat logs yet)",
    "chat.logsLoadError": "Failed to load logs: {error}",
    "chat.updatedAt": "(updated: {date})",
    "chat.memoriesLoadError": "Failed to load memories: {error}",
    "chat.memoryDeleteError": "Failed to delete memories: {error}",
    "chat.cleared": "Chat cleared",
    "chat.clearError": "Error: {error}",
    "action.delete": "Delete",
    "p2p.nativeSubtitle":
      "Native P2P: discovery, hybrid distribution and temporary cache",
    "p2p.status": "Status",
    "p2p.transfers": "Transfers in Progress",
    "p2p.openFolder": "Open P2P downloads folder",
    "p2p.artifactsLocal": "Local Artifacts",
    "p2p.peersDiscovered": "Discovered Peers",
    "p2p.configuration": "P2P Configuration",
    "p2p.audit": "Audit",
    "p2p.selectFileToPublish": "Select File to Publish",
    "p2p.deleteAll": "Delete All",
    "p2p.artifact": "Artifact",
    "p2p.size": "Size",
    "p2p.checksum": "Checksum",
    "p2p.agent": "Agent",
    "p2p.address": "Address",
    "p2p.sourceSlash": "Source",
    "p2p.actions": "Actions",
    "p2p.viewArtifacts": "View artifacts from this peer",
    "p2p.noPeers": "No peers discovered.",
    "p2p.noArtifacts": "No local artifacts.",
    "p2p.noAudit": "No activity recorded.",
    "p2p.artifactsAvailable": "Available artifacts at this peer",
    "p2p.pull": "Pull",
    "p2p.save": "Save",
    "p2p.refresh": "Refresh",
    "p2p.cleanCache": "Clear Cache",
    "p2p.cleanAll": "Clear All",
    "p2p.scanPeers": "Scan network for peers",
    "p2p.active": "Active",
    "p2p.discovery": "Discovery",
    "p2p.ttl": "TTL (hours)",
    "p2p.seedPercent": "Seed %",
    "p2p.minSeeds": "Min seeds",
    "p2p.tokenMinutes": "Token (min)",
    "p2p.sharedSecret": "Shared Secret (optional)",
    "p2p.sharedSecretHint": "enables /p2p/replicate endpoint authentication",
    "p2p.auditAllActions": "All actions",
    "p2p.auditAllPeers": "All peers",
    "p2p.auditAllStatus": "All statuses",
    "p2p.auditSuccess": "success",
    "p2p.auditError": "error",
    "p2p.loadingData": "Loading P2P data...",
    "p2p.loadError": "Failed to load P2P data: {error}",
    "psadt.title": "PSADT Debug Window",
    "psadt.subtitle":
      "Real-time diagnostics for PSAppDeployToolkit notifications, module and execution.",
    "psadt.refreshState": "Refresh State",
    "psadt.currentState": "Current State",
    "psadt.moduleTitle": "PSADT Module",
    "psadt.desiredVersion": "Desired version",
    "psadt.source": "Source",
    "psadt.checkModule": "Check Module",
    "psadt.installModule": "Install Module",
    "psadt.themePreview": "Preview Theme",
    "psadt.notificationTitle": "Title",
    "psadt.message": "Message",
    "psadt.severity": "Severity",
    "psadt.mode": "Mode",
    "psadt.layout": "Layout",
    "psadt.emitNotification": "Emit Notification",
    "psadt.close": "Close",
    "psadt.previewHistory": "Preview History",
    "ztc.subtitle":
      "Zero-Touch Config: agents without credentials are provisioned via P2P network",
    "ztc.description":
      "Agents installed with a generic installer (no predefined credentials) discover this agent via P2P network and receive configuration automatically when the feature is active on the server.",
    "ztc.recentEvents": "Recent provisioning events",
    "ztc.noEvents": "No Zero-Touch provisioning events recorded.",
    "ztc.loading": "Loading Zero-Touch provisioning data...",
  },
};

var appLocaleInitPromise = null;
var appTranslationObserver = null;

function normalizeAppLocale(raw) {
  var value = String(raw || "")
    .trim()
    .replace(/_/g, "-")
    .toLowerCase();
  if (!value) return "pt-br";
  if (value.indexOf("en") === 0) return "en-us";
  if (value.indexOf("pt") === 0) return "pt-br";
  return "pt-br";
}

function getAppLocaleTag(locale) {
  return APP_LOCALE_TAGS[normalizeAppLocale(locale)] || "pt-BR";
}

function getAppLocale() {
  var root = typeof document !== "undefined" ? document.documentElement : null;
  return normalizeAppLocale(root && root.lang ? root.lang : "pt-BR");
}

function setDocumentLocale(localeRaw) {
  var normalized = normalizeAppLocale(localeRaw);
  if (typeof document !== "undefined" && document.documentElement) {
    document.documentElement.lang = getAppLocaleTag(normalized);
  }
  return normalized;
}

function detectClientLocale() {
  var candidates = [];
  if (typeof navigator !== "undefined") {
    if (Array.isArray(navigator.languages)) {
      candidates = candidates.concat(navigator.languages);
    }
    candidates.push(navigator.language);
    candidates.push(navigator.userLanguage);
  }
  if (typeof document !== "undefined" && document.documentElement) {
    candidates.push(document.documentElement.lang);
  }

  for (var i = 0; i < candidates.length; i += 1) {
    var normalized = normalizeAppLocale(candidates[i]);
    if (APP_I18N_DICTIONARY[normalized]) {
      return normalized;
    }
  }
  return "pt-br";
}

function translate(key, replacements) {
  var locale = getAppLocale();
  var dict = APP_I18N_DICTIONARY[locale] || APP_I18N_DICTIONARY["pt-br"];
  var template = dict[key];
  if (template === undefined) {
    template = APP_I18N_DICTIONARY["pt-br"][key];
  }
  if (template === undefined) {
    return String(key || "");
  }
  return String(template).replace(
    /\{([a-zA-Z0-9_]+)\}/g,
    function (_match, token) {
      if (
        !replacements ||
        replacements[token] === undefined ||
        replacements[token] === null
      ) {
        return "";
      }
      return String(replacements[token]);
    },
  );
}

function applyTranslations(root) {
  var scope = root || document;
  if (!scope || !scope.querySelectorAll) return;
  var localeTag = getAppLocaleTag(getAppLocale());

  if (
    scope.nodeType === 1 &&
    scope.hasAttribute &&
    scope.hasAttribute("data-app-lang")
  ) {
    scope.setAttribute("lang", localeTag);
  }

  var localeNodes = scope.querySelectorAll("[data-app-lang]");
  for (var n = 0; n < localeNodes.length; n += 1) {
    localeNodes[n].setAttribute("lang", localeTag);
  }

  var textNodes = scope.querySelectorAll("[data-i18n]");
  for (var i = 0; i < textNodes.length; i += 1) {
    var textEl = textNodes[i];
    textEl.textContent = translate(textEl.getAttribute("data-i18n"));
  }

  var titleNodes = scope.querySelectorAll("[data-i18n-title]");
  for (var j = 0; j < titleNodes.length; j += 1) {
    var titleEl = titleNodes[j];
    titleEl.setAttribute(
      "title",
      translate(titleEl.getAttribute("data-i18n-title")),
    );
  }

  var ariaNodes = scope.querySelectorAll("[data-i18n-aria-label]");
  for (var k = 0; k < ariaNodes.length; k += 1) {
    var ariaEl = ariaNodes[k];
    ariaEl.setAttribute(
      "aria-label",
      translate(ariaEl.getAttribute("data-i18n-aria-label")),
    );
  }

  var placeholderNodes = scope.querySelectorAll("[data-i18n-placeholder]");
  for (var m = 0; m < placeholderNodes.length; m += 1) {
    var placeholderEl = placeholderNodes[m];
    placeholderEl.setAttribute(
      "placeholder",
      translate(placeholderEl.getAttribute("data-i18n-placeholder")),
    );
  }
}

function ensureTranslationObserver() {
  if (
    appTranslationObserver ||
    typeof MutationObserver === "undefined" ||
    !document ||
    !document.body
  ) {
    return;
  }

  appTranslationObserver = new MutationObserver(function (mutations) {
    for (var i = 0; i < mutations.length; i += 1) {
      var mutation = mutations[i];
      if (!mutation.addedNodes || !mutation.addedNodes.length) continue;
      for (var j = 0; j < mutation.addedNodes.length; j += 1) {
        var node = mutation.addedNodes[j];
        if (!node || node.nodeType !== 1) continue;
        var hasI18nAttrs =
          node.matches &&
          node.matches(
            "[data-i18n], [data-i18n-title], [data-i18n-aria-label], [data-i18n-placeholder]",
          );
        var hasI18nDescendants =
          node.querySelector &&
          node.querySelector(
            "[data-i18n], [data-i18n-title], [data-i18n-aria-label], [data-i18n-placeholder]",
          );
        if (hasI18nAttrs || hasI18nDescendants) {
          applyTranslations(node);
        }
      }
    }
  });

  appTranslationObserver.observe(document.body, {
    childList: true,
    subtree: true,
  });
}

function initApplicationLocale() {
  if (appLocaleInitPromise) return appLocaleInitPromise;

  appLocaleInitPromise = Promise.resolve().then(function () {
    var fallbackLocale = detectClientLocale();

    if (
      !(
        window.go &&
        window.go.app &&
        window.go.app.App &&
        typeof window.go.app.App.GetPreferredLocale === "function"
      )
    ) {
      setDocumentLocale(fallbackLocale || "pt-BR");
      applyTranslations(document);
      ensureTranslationObserver();
      return getAppLocaleTag(getAppLocale());
    }

    return window.go.app.App.GetPreferredLocale()
      .then(function (locale) {
        setDocumentLocale(locale || fallbackLocale || "pt-BR");
        applyTranslations(document);
        ensureTranslationObserver();
        return getAppLocaleTag(getAppLocale());
      })
      .catch(function () {
        setDocumentLocale(fallbackLocale || "pt-BR");
        applyTranslations(document);
        ensureTranslationObserver();
        return getAppLocaleTag(getAppLocale());
      });
  });

  return appLocaleInitPromise;
}

function debounce(fn, delayMs) {
  var timeoutId;
  return function () {
    var ctx = this;
    var args = arguments;
    clearTimeout(timeoutId);
    timeoutId = setTimeout(function () {
      fn.apply(ctx, args);
    }, delayMs);
  };
}

/**
 * formatDate — função canônica de formatação de data/hora para o locale ativo da aplicação.
 * Aceita string ISO 8601, Date, timestamp Unix (number) ou string legível.
 * Retorna fallback quando o valor é inválido ou ausente.
 *
 * @param {string|number|Date} value - valor de data a formatar
 * @param {string} [fallback='-'] - valor retornado para datas inválidas/ausentes
 * @returns {string}
 */
function formatDate(value, fallback) {
  if (fallback === undefined) fallback = "-";
  if (!value && value !== 0) return fallback;
  var d = value instanceof Date ? value : new Date(value);
  if (isNaN(d.getTime())) return String(value);
  return d.toLocaleString(getAppLocaleTag(getAppLocale()));
}

function getPaginationState(items, currentPage, pageSize) {
  var totalPages = Math.max(1, Math.ceil(items.length / pageSize));
  var validPage = Math.max(1, Math.min(currentPage, totalPages));
  var start = (validPage - 1) * pageSize;
  return { totalPages: totalPages, validPage: validPage, start: start };
}

function escapeHtml(value) {
  return String(value)
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

function escapeHtmlAttr(value) {
  return escapeHtml(value).replaceAll("`", "");
}

function syncColorMode() {
  var htmlEl = document.documentElement;
  if (!htmlEl) return;
  var theme = htmlEl.getAttribute("data-theme");
  var mode = theme === "dark" ? "dark" : "light";
  htmlEl.setAttribute("data-color-mode", mode);
}

function normalizeKbScope(scope) {
  var v = String(scope || "")
    .trim()
    .toLowerCase();
  if (v === "global") return "Global";
  if (v === "client") return "Cliente";
  if (v === "site") return "Site";
  return scope || "-";
}

function renderInlineMarkdown(text) {
  var html = escapeHtml(text || "");
  html = html.replace(/`([^`]+)`/g, "<code>$1</code>");
  html = html.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
  html = html.replace(/\*([^*]+)\*/g, "<em>$1</em>");
  html = html.replace(/\[([^\]]+)\]\(([^)]+)\)/g, function (_m, label, rawUrl) {
    var href = String(rawUrl || "").trim();
    var safeHref = "#";
    if (/^https?:\/\//i.test(href) || /^mailto:/i.test(href)) {
      safeHref = href;
    }
    return (
      '<a href="' +
      escapeHtmlAttr(safeHref) +
      '" target="_blank" rel="noopener noreferrer">' +
      label +
      "</a>"
    );
  });
  return html;
}

function splitMarkdownTableRow(line) {
  var raw = String(line || "").trim();
  if (raw.startsWith("|")) raw = raw.slice(1);
  if (raw.endsWith("|")) raw = raw.slice(0, -1);
  return raw.split("|").map(function (c) {
    return c.trim();
  });
}

function isMarkdownTableSeparator(line) {
  var raw = String(line || "").trim();
  if (!raw.includes("|")) return false;
  if (raw.startsWith("|")) raw = raw.slice(1);
  if (raw.endsWith("|")) raw = raw.slice(0, -1);
  var cells = raw.split("|").map(function (c) {
    return c.trim();
  });
  if (!cells.length) return false;
  for (var i = 0; i < cells.length; i++) {
    if (!/^:?-{3,}:?$/.test(cells[i])) return false;
  }
  return true;
}

function getTableAlignments(separatorLine) {
  var raw = String(separatorLine || "").trim();
  if (raw.startsWith("|")) raw = raw.slice(1);
  if (raw.endsWith("|")) raw = raw.slice(0, -1);
  var cells = raw.split("|").map(function (c) {
    return c.trim();
  });
  return cells.map(function (c) {
    var left = c.startsWith(":");
    var right = c.endsWith(":");
    if (left && right) return "center";
    if (right) return "right";
    if (left) return "left";
    return "";
  });
}

function escapeRegExp(text) {
  return String(text).replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function applySyntaxToken(html, regex, klass) {
  return html.replace(regex, function (m) {
    return '<span class="' + klass + '">' + m + "</span>";
  });
}

function highlightCodeBasic(code, lang) {
  var normalizedLang = String(lang || "")
    .trim()
    .toLowerCase();
  var html = escapeHtml(code || "");

  html = applySyntaxToken(
    html,
    /("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*')/g,
    "kb-syntax-str",
  );
  html = applySyntaxToken(html, /\b\d+(?:\.\d+)?\b/g, "kb-syntax-num");

  if (normalizedLang === "json") {
    html = applySyntaxToken(html, /"([^"\\]|\\.)*"(?=\s*:)/g, "kb-syntax-key");
    html = applySyntaxToken(html, /\b(true|false|null)\b/g, "kb-syntax-kw");
    return html;
  }

  if (
    normalizedLang === "bash" ||
    normalizedLang === "sh" ||
    normalizedLang === "powershell" ||
    normalizedLang === "ps1"
  ) {
    html = applySyntaxToken(html, /#[^\n]*/g, "kb-syntax-com");
    html = applySyntaxToken(
      html,
      /\b(if|then|else|fi|for|do|done|while|case|esac|function|return|exit)\b/g,
      "kb-syntax-kw",
    );
    return html;
  }

  html = applySyntaxToken(html, /\/\/[^\n]*/g, "kb-syntax-com");
  html = applySyntaxToken(
    html,
    /\b(func|function|return|if|else|switch|case|default|for|range|break|continue|const|let|var|type|struct|interface|map|chan|package|import|try|catch|throw|new|class|public|private|protected|static|async|await|nil|true|false)\b/g,
    "kb-syntax-kw",
  );

  if (normalizedLang === "go") {
    var goTypes = [
      "string",
      "int",
      "int64",
      "float64",
      "bool",
      "byte",
      "rune",
      "error",
      "any",
    ];
    var goPattern = new RegExp(
      "\\b(" + goTypes.map(escapeRegExp).join("|") + ")\\b",
      "g",
    );
    html = applySyntaxToken(html, goPattern, "kb-syntax-typ");
  }

  return html;
}

function renderMarkdown(markdown) {
  var lines = String(markdown || "")
    .replace(/\r\n/g, "\n")
    .split("\n");
  var html = [];
  html.push('<div class="md-content">');
  var inCode = false;
  var inCodeLang = "";
  var codeLines = [];
  var paragraph = [];
  var listType = "";

  function flushParagraph() {
    if (!paragraph.length) return;
    html.push("<p>" + renderInlineMarkdown(paragraph.join(" ")) + "</p>");
    paragraph = [];
  }

  function closeList() {
    if (!listType) return;
    html.push("</" + listType + ">");
    listType = "";
  }

  function renderCodeBlock(lines, fenceLang) {
    var lang = String(fenceLang || "").trim();
    var className = lang
      ? ' class="language-' + escapeHtmlAttr(lang.toLowerCase()) + '"'
      : "";
    var highlighted = highlightCodeBasic(lines.join("\n"), lang);
    return "<pre><code" + className + ">" + highlighted + "</code></pre>";
  }

  for (var i = 0; i < lines.length; i++) {
    var line = lines[i];
    var trimmed = line.trim();

    if (trimmed.startsWith("```")) {
      flushParagraph();
      closeList();
      if (!inCode) {
        inCode = true;
        var fenceInfo = trimmed.slice(3).trim();
        inCodeLang = fenceInfo ? fenceInfo.split(/\s+/)[0] : "";
        codeLines = [];
      } else {
        html.push(renderCodeBlock(codeLines, inCodeLang));
        inCode = false;
        inCodeLang = "";
      }
      continue;
    }

    if (inCode) {
      codeLines.push(line);
      continue;
    }

    if (!trimmed) {
      flushParagraph();
      closeList();
      continue;
    }

    var heading = trimmed.match(/^(#{1,6})\s+(.+)$/);
    if (heading) {
      flushParagraph();
      closeList();
      var level = heading[1].length;
      html.push(
        "<h" +
          level +
          ">" +
          renderInlineMarkdown(heading[2]) +
          "</h" +
          level +
          ">",
      );
      continue;
    }

    if (
      trimmed.includes("|") &&
      i + 1 < lines.length &&
      isMarkdownTableSeparator(lines[i + 1])
    ) {
      flushParagraph();
      closeList();

      var headers = splitMarkdownTableRow(trimmed);
      var alignments = getTableAlignments(lines[i + 1]);
      html.push(
        '<div class="kb-table-wrap"><table class="kb-markdown-table"><thead><tr>',
      );
      for (var h = 0; h < headers.length; h++) {
        var hAlign = alignments[h]
          ? ' style="text-align:' + alignments[h] + '"'
          : "";
        html.push(
          "<th" + hAlign + ">" + renderInlineMarkdown(headers[h]) + "</th>",
        );
      }
      html.push("</tr></thead><tbody>");

      i += 2;
      for (; i < lines.length; i++) {
        var rowTrimmed = String(lines[i] || "").trim();
        if (!rowTrimmed || !rowTrimmed.includes("|")) {
          i -= 1;
          break;
        }
        var rowCells = splitMarkdownTableRow(rowTrimmed);
        html.push("<tr>");
        for (var c = 0; c < headers.length; c++) {
          var cell = c < rowCells.length ? rowCells[c] : "";
          var align = alignments[c]
            ? ' style="text-align:' + alignments[c] + '"'
            : "";
          html.push("<td" + align + ">" + renderInlineMarkdown(cell) + "</td>");
        }
        html.push("</tr>");
      }
      html.push("</tbody></table></div>");
      continue;
    }

    var unordered = trimmed.match(/^[-*]\s+(.+)$/);
    if (unordered) {
      flushParagraph();
      if (listType !== "ul") {
        closeList();
        listType = "ul";
        html.push("<ul>");
      }
      html.push("<li>" + renderInlineMarkdown(unordered[1]) + "</li>");
      continue;
    }

    var ordered = trimmed.match(/^\d+\.\s+(.+)$/);
    if (ordered) {
      flushParagraph();
      if (listType !== "ol") {
        closeList();
        listType = "ol";
        html.push("<ol>");
      }
      html.push("<li>" + renderInlineMarkdown(ordered[1]) + "</li>");
      continue;
    }

    closeList();
    paragraph.push(trimmed);
  }

  if (inCode) {
    html.push(renderCodeBlock(codeLines, inCodeLang));
  }
  flushParagraph();
  closeList();

  html.push("</div>");
  return html.join("");
}

function buildKnowledgeMeta(article) {
  var parts = [];
  if (article.category)
    parts.push(
      '<span class="kb-meta-cat">' + escapeHtml(article.category) + "</span>",
    );
  if (article.scope) {
    var s = normalizeKbScope(article.scope);
    if (s && s !== "-")
      parts.push("<span>Escopo: " + escapeHtml(s) + "</span>");
  }
  if (article.author)
    parts.push("<span>Autor: " + escapeHtml(article.author) + "</span>");
  if (article.difficulty)
    parts.push("<span>Nivel: " + escapeHtml(article.difficulty) + "</span>");
  if (article.readTimeMin)
    parts.push(
      "<span>Leitura: " +
        escapeHtml(String(article.readTimeMin)) +
        " min</span>",
    );
  if (article.updatedAt)
    parts.push(
      "<span>Atualizado: " +
        escapeHtml(formatDate(article.updatedAt, "-")) +
        "</span>",
    );
  return parts.join("");
}

function getDiskUsagePercent(disk) {
  if (!disk.freeKnown) return null;
  var size = Number(disk.sizeGB || 0);
  var free = Number(disk.freeGB || 0);
  if (!Number.isFinite(size) || size <= 0) return 0;
  var used = Math.max(0, size - free);
  var pct = Math.round((used / size) * 100);
  return Math.min(100, Math.max(0, pct));
}

function renderDiskUsageBar(disk) {
  var usage = getDiskUsagePercent(disk);
  if (usage === null) return "";
  return (
    '<div class="disk-bar"><span style="width: ' +
    escapeHtmlAttr(String(usage)) +
    '%"></span></div>'
  );
}

function renderDiskUsageLabel(disk) {
  var usage = getDiskUsagePercent(disk);
  if (usage === null) return "Uso: indisponivel";
  var freePct = 100 - usage;
  return "Uso: " + usage + "% | Livre: " + freePct + "%";
}

function renderDiskOccupiedGB(disk) {
  if (!disk.freeKnown) return "indisponivel";
  var size = Number(disk.sizeGB || 0);
  var free = Number(disk.freeGB || 0);
  if (!Number.isFinite(size) || !Number.isFinite(free) || size <= 0)
    return "indisponivel";
  var occupied = Math.max(0, size - free);
  return occupied.toFixed(2) + " GB";
}
