"use strict";

var APP_LOCALE_TAGS = {
  'pt-br': 'pt-BR',
  'en-us': 'en-US',
};

var APP_I18N_DICTIONARY = {
  'pt-br': {
    'app.name': 'Discovery',
    'action.collapseMenu': 'Colapsar menu',
    'action.maximizeRestore': 'Maximizar ou restaurar',
    'action.closeToTray': 'Fechar para tray',
    'action.toggleTheme': 'Alternar tema',
    'action.refresh': 'Atualizar',
    'action.refreshList': 'Atualizar Lista',
    'action.refreshStatus': 'Atualizar Status',
    'action.reloadCatalog': 'Recarregar Catalogo',
    'action.checkUpdates': 'Verificar Atualizacoes',
    'action.upgradeSelected': 'Atualizar Selecionados',
    'action.selectAll': 'Selecionar todos',
    'action.previous': 'Anterior',
    'action.next': 'Proxima',
    'action.close': 'Fechar',
    'action.install': 'Instalar',
    'action.remove': 'Remover',
    'action.send': 'Enviar',
    'action.stop': 'Stop',
    'action.clear': 'Limpar',
    'action.clearChat': 'Limpar Chat',
    'action.openFullscreen': 'Abrir em tela cheia',
    'action.viewTools': 'Ver Ferramentas',
    'action.testConfiguration': 'Testar Configuracao',
    'action.saveConfiguration': 'Salvar Configuracao',
    'action.testConnection': 'Testar Conexao',
    'action.goToP2P': 'Ir para P2P',
    'action.goToPSADT': 'Ir para PSADT',
    'action.collectInventory': 'Coletar Inventario',
    'action.refreshPolicy': 'Atualizar Policy',
    'action.newTicket': '+ Novo Chamado',
    'action.sendTicket': 'Enviar Chamado',
    'action.sendComment': 'Enviar Comentario',
    'action.closeTicket': 'Fechar Chamado',
    'action.backToForm': 'Voltar ao formulario',
    'action.openP2PDebug': 'Abrir P2P Debug',
    'tab.status': 'Status',
    'tab.store': 'Loja',
    'tab.updates': 'Atualizacoes',
    'tab.inventory': 'Inventario',
    'tab.logs': 'Logs',
    'tab.chat': 'Chat IA',
    'tab.support': 'Suporte',
    'tab.knowledge': 'Base de Conhecimento',
    'tab.automation': 'Automacao',
    'tab.debug': 'Debug',
    'tab.psadt': 'PSADT',
    'tab.p2p': 'P2P',
    'theme.dark': 'Tema Escuro',
    'theme.light': 'Tema Claro',
    'topbar.service': 'Service',
    'topbar.serviceHealthTitle': 'Status do Service (Processo Headless)',
    'window.meta.pc': 'PC',
    'window.meta.server': 'Servidor',
    'window.meta.connection': 'Conexao',
    'common.loading': 'Carregando...',
    'common.loadingStatus': 'Carregando status...',
    'common.verifying': 'Verificando...',
    'common.awaiting': 'Aguardando...',
    'common.online': 'Online',
    'common.offline': 'Offline',
    'common.degraded': 'Degradado',
    'common.unavailable': 'Indisponivel',
    'common.healthy': 'Saudavel',
    'common.noAdditionalInfo': 'Sem informacoes adicionais.',
    'common.noOutput': '(sem saida)',
    'common.unknown': 'Desconhecido',
    'common.notAvailable': 'N/A',
    'common.error': 'Erro',
    'common.yes': 'Sim',
    'common.no': 'Nao',
    'pagination.pageOne': 'Pagina 1',
    'pagination.page': 'Pagina {page} de {total}',
    'field.name': 'Nome',
    'field.type': 'Tipo',
    'field.source': 'Origem',
    'field.status': 'Status',
    'field.user': 'Usuario',
    'field.version': 'Versao',
    'field.publisher': 'Publisher',
    'field.installId': 'ID Instalacao',
    'field.serial': 'Serial',
    'field.origin': 'Origem',
    'field.action': 'Acao',
    'field.currentVersion': 'Versao Atual',
    'field.availableVersion': 'Versao Disponivel',
    'field.category': 'Categoria',
    'field.priority': 'Prioridade',
    'field.description': 'Descricao',
    'field.rating': 'Avaliacao',
    'field.process': 'Processo',
    'field.protocol': 'Protocolo',
    'field.address': 'Endereco',
    'field.port': 'Porta',
    'field.local': 'Local',
    'field.remote': 'Remoto',
    'field.id': 'ID',
    'field.path': 'Path',
    'field.args': 'Args',
    'field.family': 'Family',
    'field.os': 'SO',
    'field.osVersion': 'Versao do SO',
    'field.components': 'Componentes',
    'field.problems': 'Problemas',
    'field.realtime': 'Realtime',
    'field.connectedAgents': 'Agentes conectados',
    'field.lastInventory': 'Ultimo inventario',
    'field.lastCheck': 'Ultima verificacao',
    'logs.empty': '(sem logs)',
    'logs.emptyOrigin': '(sem logs para a origem selecionada)',
    'logs.cleared': 'Logs limpos',
    'logs.clearError': 'Erro ao limpar logs: {error}',
    'logs.subtitle': 'Terminal de logs',
    'logs.filterByOrigin': 'Filtrar logs por origem',
    'logs.origin.all': 'Todas',
    'logs.origin.other': 'Outros',
    'provisioning.title': 'Agente aguardando provisionamento',
    'provisioning.message': 'Este agente esta aguardando provisionamento pela equipe de TI ou suporte.',
    'provisioning.footnote': 'Se esta mensagem persistir, entre em contato com a equipe de TI ou suporte.',
    'provisioning.completed': 'Provisionamento concluido. Interface liberada.',
    'provisioning.refresh': 'Verificar novamente',
    'status.summary': 'Resumo rapido de saude do agente',
    'status.connectionTitle': 'Conexao do Agente',
    'status.awaitingRead': 'Aguardando leitura do status.',
    'status.applicationTitle': 'Aplicacao',
    'status.systemTitle': 'Sistema',
    'status.serviceTitle': 'Service (Processo Headless)',
    'status.realtimeTitle': 'Realtime e Integracao',
    'status.localComputer': 'Computador local',
    'status.failedRead': 'Falha na leitura de status',
    'status.couldNotLoadAgentStatus': 'Nao foi possivel carregar o status do agente.',
    'status.serviceUnavailable': 'Service indisponivel',
    'status.serviceUnavailableDetail': 'Nao foi possivel comunicar com o servico Discovery. Reinicie o computador e tente novamente. Se o problema persistir, contate o suporte.',
    'status.notRunning': 'Nao esta rodando',
    'status.problemDetected': 'Problema detectado',
    'status.allComponentsHealthy': 'Todos os componentes estao saudaveis',
    'status.problemsIn': 'Problemas em: {components}',
    'status.serviceIndicator': 'Service: {label}',
    'store.searchPlaceholder': 'Pesquisar aplicativo por nome, id ou publisher',
    'store.synced': 'Loja atualizada por sync{variant}',
    'store.newDataSync': 'Novos dados da loja recebidos via sync',
    'store.noPackagesFound': 'Nenhum pacote encontrado',
    'store.adjustSearchFilter': 'Ajuste o filtro de busca.',
    'store.noDescription': 'Sem descricao',
    'store.viewDetails': 'Ver detalhes',
    'store.viewDetailsOf': 'Ver detalhes de {name}',
    'store.packageId': 'ID: {id}',
    'store.catalogLoading': 'Carregando catalogo...',
    'store.catalogLoaded': 'Catalogo carregado.',
    'store.appsAllowed': 'Aplicativos disponiveis: {count}',
    'store.catalogLoadFailure': 'Falha ao carregar apps permitidos',
    'store.appMeta': '{publisher} | {version} | ID: {id}',
    'updates.unchecked': 'Atualizacoes nao verificadas',
    'updates.checking': 'Verificando atualizacoes pendentes...',
    'updates.clickCheckToList': 'Clique em "Verificar Atualizacoes" para listar.',
    'updates.availableCount': '{count} atualizacao(oes) disponivel(is)',
    'updates.foundCount': '{count} atualizacao(oes) encontrada(s)',
    'updates.nonePending': 'Nenhuma atualizacao pendente',
    'updates.checkError': 'Erro ao verificar atualizacoes',
    'updates.upgrade': 'Atualizar',
    'updates.upgradingItem': 'Atualizando {id}...',
    'updates.upgradeSuccess': '{id} atualizado com sucesso',
    'updates.upgradeError': 'Erro ao atualizar {id}: {error}',
    'updates.batchComplete': 'Atualizacao em lote concluida',
    'inventory.notLoaded': 'Inventario nao carregado',
    'inventory.collecting': 'Coletando dados de inventario...',
    'inventory.initialLoadingText': 'Buscando cache local e preparando os dados para exibicao.',
    'inventory.hardware': 'Hardware',
    'inventory.operatingSystem': 'Sistema Operacional',
    'inventory.loggedUsers': 'Usuarios Logados',
    'inventory.volumes': 'Volumes (logical_drives)',
    'inventory.networkInterfaces': 'Interfaces de Rede',
    'inventory.printers': 'Impressoras',
    'inventory.memoryModules': 'Memoria (Pentes)',
    'inventory.monitors': 'Monitores',
    'inventory.gpu': 'GPU',
    'inventory.battery': 'Bateria',
    'inventory.bitlocker': 'BitLocker',
    'inventory.cpuInfo': 'CPU Info',
    'inventory.startupItems': 'Startup Items',
    'inventory.refreshStartupItems': 'Atualizar startup items',
    'inventory.searchStartupPlaceholder': 'Pesquisar startup item por nome, path, usuario ou origem',
    'inventory.autoexec': 'Autoexec',
    'inventory.installedSoftware': 'Softwares Instalados',
    'inventory.refreshSoftware': 'Atualizar softwares',
    'inventory.searchSoftwarePlaceholder': 'Pesquisar software por nome, versao ou publisher',
    'inventory.networkConnections': 'Conexoes de Rede',
    'inventory.listeningPorts': 'Portas em Escuta',
    'inventory.openConnections': 'Conexoes Abertas',
    'inventory.refreshConnections': 'Atualizar portas e conexoes',
    'inventory.refreshListeningPorts': 'Atualizar portas em escuta',
    'inventory.searchConnectionsPlaceholder': 'Pesquisar por processo, porta, endereco...',
    'inventory.noneSoftwareFound': 'Nenhum software encontrado.',
    'inventory.noneStartupFound': 'Nenhum startup item encontrado.',
    'inventory.noneListeningPortFound': 'Nenhuma porta encontrada.',
    'inventory.noneOpenConnectionFound': 'Nenhuma conexao encontrada.',
    'inventory.visibleTotal': 'Total visivel: {visible}',
    'inventory.visibleInventoryTotal': 'Total visivel: {visible} | Total inventario: {total}',
    'inventory.collectingNow': 'Coletando inventario...',
    'inventory.collectedAt': 'Coletado em {date}',
    'inventory.updated': 'Inventario atualizado.',
    'inventory.collectFailed': 'Falha ao coletar inventario',
    'inventory.initialCollectFailed': 'Falha ao coletar os dados iniciais. Clique em Coletar Inventario para tentar novamente.',
    'inventory.exportPdfRunningFeedback': 'Exportando inventario em PDF...',
    'inventory.exportPdfRunningStatus': 'Exportacao PDF em andamento...',
    'inventory.exportNoFile': 'Nenhum arquivo retornado do servidor',
    'inventory.exportNoPath': 'Falha: nenhum caminho retornado',
    'inventory.exportPdfSuccessFeedback': 'PDF exportado: {path}',
    'inventory.exportPdfSuccessStatus': 'PDF criado com sucesso em: {path}',
    'inventory.exportPdfError': 'Falha ao exportar PDF: {error}',
    'inventory.osqueryInstalled': 'osquery: instalado ({path})',
    'inventory.osqueryNotDetected': 'osquery: nao detectado (pacote: {packageId})',
    'inventory.osqueryCheckError': 'osquery: erro ao verificar ({error})',
    'inventory.installingOsquery': 'Instalando osquery via winget...',
    'inventory.osqueryInstalledFeedback': 'Instalacao do osquery concluida.',
    'support.title': 'Suporte',
    'support.subtitle': 'Chamados vinculados a este agente',
    'support.myTickets': 'Meus Chamados',
    'support.noTickets': 'Nenhum chamado.',
    'support.closeTicketTitle': 'Fechar Chamado',
    'support.noRating': 'Sem avaliacao',
    'support.finalStateOptional': 'Estado final (opcional)',
    'support.closeWithDefaultState': 'Fechar com estado padrao',
    'support.customFinalStateGuidPlaceholder': 'GUID do estado final customizado',
    'support.closingCommentOptional': 'Comentario de fechamento (opcional)',
    'support.describeAppliedSolution': 'Descreva a solucao aplicada...',
    'support.comments': 'Comentarios',
    'support.writeCommentPlaceholder': 'Escreva um comentario...',
    'support.openTicketTitle': 'Abrir Chamado',
    'support.ticketTitle': 'Titulo *',
    'support.problemSummary': 'Resumo do problema',
    'support.select': 'Selecione...',
    'support.category.hardware': 'Hardware',
    'support.category.software': 'Software',
    'support.category.network': 'Rede',
    'support.category.access': 'Acesso / Permissoes',
    'support.category.email': 'E-mail',
    'support.category.printer': 'Impressora',
    'support.category.vpn': 'VPN',
    'support.category.other': 'Outro',
    'support.priority.low': 'Baixa',
    'support.priority.medium': 'Media',
    'support.priority.high': 'Alta',
    'support.priority.critical': 'Critica',
    'support.descriptionRequired': 'Descricao *',
    'support.describeProblemDetails': 'Descreva o problema com detalhes...',
    'support.fillTitleDescription': 'Preencha titulo e descricao',
    'support.sending': 'Enviando...',
    'support.submittingTicket': 'Enviando chamado...',
    'support.ticketCreatedSuccess': 'Chamado criado com sucesso!',
    'support.ticketCreateError': 'Erro ao criar chamado: {error}',
    'support.ticketListLoadError': 'Erro ao carregar chamados: {error}',
    'support.enterComment': 'Digite um comentario',
    'support.commentSent': 'Comentario enviado',
    'support.commentSendError': 'Erro ao enviar comentario: {error}',
    'support.invalidRating': 'Avaliacao invalida. Informe de 0 a 5.',
    'support.closing': 'Fechando...',
    'support.ticketClosedSuccess': 'Chamado fechado com sucesso',
    'support.ticketCloseError': 'Erro ao fechar chamado: {error}',
    'support.agentLabel': 'Agente: {name}',
    'support.serverNotConfigured': 'Servidor nao configurado. Configure em Debug.',
    'support.noTicketsPrompt': 'Nenhum chamado no momento. Clique em "+ Novo Chamado" para abrir um.',
    'support.defaultOpenStatus': 'Aberto',
    'support.ticketOpenedAt': 'Aberto em: {date}',
    'support.ratingDisplay': 'Avaliacao: {rating}',
    'support.ratedAt': 'Avaliado em: {date}',
    'support.ratedBy': 'Avaliado por: {name}',
    'support.loadingComments': 'Carregando comentarios...',
    'support.noComments': 'Nenhum comentario.',
    'support.commentLoadError': 'Erro ao carregar comentarios: {error}',
    'support.user': 'Usuario',
    'support.internal': 'Interno',
    'knowledge.title': 'Base de Conhecimento',
    'knowledge.subtitle': 'Artigos de manutencao e troubleshooting de PCs',
    'knowledge.searchPlaceholder': 'Buscar por titulo, categoria, tag ou conteudo',
    'knowledge.loadingArticles': 'Carregando artigos...',
    'knowledge.article': 'Artigo',
    'knowledge.noArticlesFound': 'Nenhum artigo encontrado.',
    'knowledge.loadError': 'Erro ao carregar base de conhecimento.',
    'automation.title': 'Automacao',
    'automation.subtitle': 'Policy sync local e tarefas aplicadas ao agent',
    'automation.includeScriptContent': 'Incluir conteudo de script no sync manual',
    'automation.notes': 'Observacoes',
    'automation.noSyncYet': 'Nenhum sync executado ainda.',
    'automation.localExecution': 'Execucao Local',
    'automation.pendingCallbacks': '{count} callbacks pendentes',
    'automation.ackResultMessage': 'ACK e RESULT sao enviados imediatamente quando possivel e entram em fila persistente quando o backend estiver indisponivel.',
    'automation.resolvedTasks': 'Tarefas Resolvidas',
    'automation.zeroTasks': '{count} tarefas',
    'automation.noPolicyLoaded': 'Nenhuma policy carregada.',
    'automation.recentExecutions': 'Execucoes Recentes',
    'automation.zeroExecutions': '{count} execucoes',
    'automation.noExecutions': 'Nenhuma execucao registrada.',
    'automation.awaitingConfig': 'Aguardando configuracao',
    'automation.synchronized': 'Sincronizado',
    'automation.noCommunication': 'Sem comunicacao',
    'automation.noChanges': 'Sem alteracoes',
    'automation.newOrLocalPolicy': 'Policy nova ou local',
    'automation.connection': 'Conexao',
    'automation.tasks': 'Tasks',
    'automation.pendingCallbacksLabel': 'Callbacks pendentes',
    'automation.upToDate': 'UpToDate',
    'automation.lastSync': 'Ultimo sync',
    'automation.lastAttempt': 'Ultima tentativa',
    'automation.localCache': 'Cache local',
    'automation.generatedAt': 'Policy gerada em {date}',
    'automation.manualSyncWithScript': 'Sync manual com conteudo de script habilitado',
    'automation.lastErrorLine': 'Ultimo erro: {error}',
    'automation.noAlerts': 'Sem alertas no momento.',
    'automation.noneResolvedTasks': 'Nenhuma tarefa resolvida para o agent.',
    'automation.action': 'Acao',
    'automation.scope': 'Escopo',
    'automation.updatedAt': 'Atualizado em',
    'automation.untitledTask': 'Tarefa sem nome',
    'automation.viewLogs': 'Ver logs',
    'automation.hideLogs': 'Ocultar logs',
    'automation.pendingCallbackChip': 'Callback pendente',
    'automation.executionTitle': 'Execucao',
    'automation.summary': 'Resumo',
    'automation.errorLabel': 'Erro',
    'automation.start': 'Inicio',
    'automation.end': 'Fim',
    'automation.inProgress': 'Em andamento',
    'automation.duration': 'Duracao',
    'automation.loadingState': 'Carregando estado da automacao...',
    'automation.waitingServerConfig': 'Automacao aguardando configuracao de servidor/token.',
    'automation.lastErrorState': 'Automacao com ultimo erro registrado.',
    'automation.policyLoadedSuccess': 'Policy carregada com sucesso.',
    'automation.policyLoadedLocal': 'Policy local carregada sem comunicacao atual com o backend.',
    'automation.loadFailed': 'Falha ao carregar automacao: {error}',
    'automation.syncingPolicy': 'Sincronizando policy...',
    'automation.syncWarning': 'Policy sincronizada com alerta.',
    'automation.syncSuccess': 'Policy sincronizada com sucesso.',
    'automation.syncFailed': 'Falha no policy sync: {error}',
    'debug.subtitle': 'Debug - configuracao de conexao com servidor remoto',
    'debug.agentStatusTitle': 'Status do Agente',
    'debug.connectionSettings': 'Configuracoes de Conexao',
    'debug.apiServer': 'Servidor API (HTTP/HTTPS)',
    'debug.authToken': 'Token de Autenticacao (API)',
    'debug.natsServer': 'Servidor NATS (Comandos)',
    'debug.agentId': 'Agent ID (NATS)',
    'debug.p2pFirstInstall': 'Automacao: instalar via P2P-first (teste)',
    'debug.p2pFalseOption': 'false (usar Winget CLI)',
    'debug.p2pTrueOption': 'true (tentar P2P e fallback Winget)',
    'debug.testResponse': 'Resposta do teste de conexao',
    'debug.offlineDisconnected': 'Offline / Desconectado',
    'debug.serverLabel': 'servidor: {server}',
    'debug.statusLoadError': 'Erro ao carregar status: {error}',
    'debug.saving': 'Salvando...',
    'debug.savedSuccess': 'Configuracao salva com sucesso.',
    'debug.saveError': 'Erro ao salvar: {error}',
    'debug.testingConnection': 'Testando conexao...',
    'debug.connectedSuccess': 'Conectado com sucesso.',
    'debug.connectionFailure': 'Falha na conexao: {error}',
    'chat.subtitle': 'Chat IA - converse com a IA para gerenciar seu computador',
    'chat.memories': 'Memorias',
    'chat.apiServerOptional': 'Servidor API (opcional)',
    'chat.agentTokenOptional': 'Token do Agente (opcional)',
    'chat.modelCompatibility': 'Modelo (compatibilidade)',
    'chat.maxTokensOptional': 'Max Tokens (opcional)',
    'chat.assistantInstructionsOptional': 'Instrucoes do Assistente (Opcional)',
    'chat.systemPromptPlaceholder': 'Ex.: Responda com foco em inventario corporativo e sempre cite riscos antes de executar acoes.',
    'chat.configUsesDebugFallback': 'Se endpoint/token ficarem vazios, o chat usa automaticamente apiScheme/apiServer/authToken da aba Debug.',
    'chat.inputPlaceholder': 'Digite sua mensagem... (Enter para enviar, Shift+Enter para nova linha)',
    'chat.logsTitle': 'Logs do Chat IA',
    'chat.localMemories': 'Memorias Locais',
    'chat.noMemoryFound': 'Nenhuma memoria encontrada.',
    'chat.stopping': 'Parando...',
    'chat.thinking': 'Pensando...',
    'chat.responseInterrupted': '_Resposta interrompida pelo usuario._',
    'chat.errorUnknown': 'Erro: {error}',
    'chat.maxTokensValidation': 'Max Tokens deve ser 0 ou maior',
    'chat.configSavedSuccess': 'Configuracao de IA salva com sucesso',
    'chat.configSaveError': 'Erro ao salvar configuracao: {error}',
    'chat.configTesting': 'Testando configuracao de IA...',
    'chat.configTestSuccess': 'Teste concluido com sucesso{suffix}',
    'chat.configTestFailure': 'Falha no teste da configuracao: {error}',
    'chat.toolsLoadError': 'Erro ao carregar ferramentas',
    'chat.noLogsYet': '(sem logs de chat ainda)',
    'chat.logsLoadError': 'Erro ao carregar logs: {error}',
    'chat.updatedAt': '(atualizado: {date})',
    'chat.memoriesLoadError': 'Erro ao carregar memorias: {error}',
    'chat.memoryDeleteError': 'Erro ao excluir memorias: {error}',
    'chat.cleared': 'Chat limpo',
    'chat.clearError': 'Erro: {error}',
    'action.delete': 'Excluir',
  },
  'en-us': {
    'app.name': 'Discovery',
    'action.collapseMenu': 'Collapse menu',
    'action.maximizeRestore': 'Maximize or restore',
    'action.closeToTray': 'Close to tray',
    'action.toggleTheme': 'Toggle theme',
    'action.refresh': 'Refresh',
    'action.refreshList': 'Refresh List',
    'action.refreshStatus': 'Refresh Status',
    'action.reloadCatalog': 'Reload Catalog',
    'action.checkUpdates': 'Check Updates',
    'action.upgradeSelected': 'Upgrade Selected',
    'action.selectAll': 'Select all',
    'action.previous': 'Previous',
    'action.next': 'Next',
    'action.close': 'Close',
    'action.install': 'Install',
    'action.remove': 'Remove',
    'action.send': 'Send',
    'action.stop': 'Stop',
    'action.clear': 'Clear',
    'action.clearChat': 'Clear Chat',
    'action.openFullscreen': 'Open full screen',
    'action.viewTools': 'View Tools',
    'action.testConfiguration': 'Test Configuration',
    'action.saveConfiguration': 'Save Configuration',
    'action.testConnection': 'Test Connection',
    'action.goToP2P': 'Go to P2P',
    'action.goToPSADT': 'Go to PSADT',
    'action.collectInventory': 'Collect Inventory',
    'action.refreshPolicy': 'Refresh Policy',
    'action.newTicket': '+ New Ticket',
    'action.sendTicket': 'Submit Ticket',
    'action.sendComment': 'Send Comment',
    'action.closeTicket': 'Close Ticket',
    'action.backToForm': 'Back to form',
    'action.openP2PDebug': 'Open P2P Debug',
    'tab.status': 'Status',
    'tab.store': 'Store',
    'tab.updates': 'Updates',
    'tab.inventory': 'Inventory',
    'tab.logs': 'Logs',
    'tab.chat': 'AI Chat',
    'tab.support': 'Support',
    'tab.knowledge': 'Knowledge Base',
    'tab.automation': 'Automation',
    'tab.debug': 'Debug',
    'tab.psadt': 'PSADT',
    'tab.p2p': 'P2P',
    'theme.dark': 'Dark theme',
    'theme.light': 'Light theme',
    'topbar.service': 'Service',
    'topbar.serviceHealthTitle': 'Service status (Headless process)',
    'window.meta.pc': 'PC',
    'window.meta.server': 'Server',
    'window.meta.connection': 'Connection',
    'common.loading': 'Loading...',
    'common.loadingStatus': 'Loading status...',
    'common.verifying': 'Checking...',
    'common.awaiting': 'Waiting...',
    'common.online': 'Online',
    'common.offline': 'Offline',
    'common.degraded': 'Degraded',
    'common.unavailable': 'Unavailable',
    'common.healthy': 'Healthy',
    'common.noAdditionalInfo': 'No additional information.',
    'common.noOutput': '(no output)',
    'common.unknown': 'Unknown',
    'common.notAvailable': 'N/A',
    'common.error': 'Error',
    'common.yes': 'Yes',
    'common.no': 'No',
    'pagination.pageOne': 'Page 1',
    'pagination.page': 'Page {page} of {total}',
    'field.name': 'Name',
    'field.type': 'Type',
    'field.source': 'Source',
    'field.status': 'Status',
    'field.user': 'User',
    'field.version': 'Version',
    'field.publisher': 'Publisher',
    'field.installId': 'Install ID',
    'field.serial': 'Serial',
    'field.origin': 'Origin',
    'field.action': 'Action',
    'field.currentVersion': 'Current Version',
    'field.availableVersion': 'Available Version',
    'field.category': 'Category',
    'field.priority': 'Priority',
    'field.description': 'Description',
    'field.rating': 'Rating',
    'field.process': 'Process',
    'field.protocol': 'Protocol',
    'field.address': 'Address',
    'field.port': 'Port',
    'field.local': 'Local',
    'field.remote': 'Remote',
    'field.id': 'ID',
    'field.path': 'Path',
    'field.args': 'Args',
    'field.family': 'Family',
    'field.os': 'OS',
    'field.osVersion': 'OS Version',
    'field.components': 'Components',
    'field.problems': 'Problems',
    'field.realtime': 'Realtime',
    'field.connectedAgents': 'Connected Agents',
    'field.lastInventory': 'Last Inventory',
    'field.lastCheck': 'Last Check',
    'logs.empty': '(no logs)',
    'logs.emptyOrigin': '(no logs for the selected source)',
    'logs.cleared': 'Logs cleared',
    'logs.clearError': 'Failed to clear logs: {error}',
    'logs.subtitle': 'Log terminal',
    'logs.filterByOrigin': 'Filter logs by origin',
    'logs.origin.all': 'All',
    'logs.origin.other': 'Other',
    'provisioning.title': 'Agent awaiting provisioning',
    'provisioning.message': 'This agent is waiting for provisioning by the IT or support team.',
    'provisioning.footnote': 'If this message persists, contact the IT or support team.',
    'provisioning.completed': 'Provisioning completed. Interface unlocked.',
    'provisioning.refresh': 'Check again',
    'status.summary': 'Quick agent health summary',
    'status.connectionTitle': 'Agent Connection',
    'status.awaitingRead': 'Waiting for status read.',
    'status.applicationTitle': 'Application',
    'status.systemTitle': 'System',
    'status.serviceTitle': 'Service (Headless Process)',
    'status.realtimeTitle': 'Realtime and Integration',
    'status.localComputer': 'Local computer',
    'status.failedRead': 'Failed to read status',
    'status.couldNotLoadAgentStatus': 'Could not load agent status.',
    'status.serviceUnavailable': 'Service unavailable',
    'status.serviceUnavailableDetail': 'Could not communicate with the Discovery service. Restart the computer and try again. If the problem persists, contact support.',
    'status.notRunning': 'Not running',
    'status.problemDetected': 'Problem detected',
    'status.allComponentsHealthy': 'All components are healthy',
    'status.problemsIn': 'Problems in: {components}',
    'status.serviceIndicator': 'Service: {label}',
    'store.searchPlaceholder': 'Search app by name, id or publisher',
    'store.synced': 'Store updated by sync{variant}',
    'store.newDataSync': 'New store data received via sync',
    'store.noPackagesFound': 'No packages found',
    'store.adjustSearchFilter': 'Adjust the search filter.',
    'store.noDescription': 'No description',
    'store.viewDetails': 'View details',
    'store.viewDetailsOf': 'View details for {name}',
    'store.packageId': 'ID: {id}',
    'store.catalogLoading': 'Loading catalog...',
    'store.catalogLoaded': 'Catalog loaded.',
    'store.appsAllowed': 'Available apps: {count}',
    'store.catalogLoadFailure': 'Failed to load allowed apps',
    'store.appMeta': '{publisher} | {version} | ID: {id}',
    'updates.unchecked': 'Updates not checked',
    'updates.checking': 'Checking pending updates...',
    'updates.clickCheckToList': 'Click "Check Updates" to list them.',
    'updates.availableCount': '{count} update(s) available',
    'updates.foundCount': '{count} update(s) found',
    'updates.nonePending': 'No pending updates',
    'updates.checkError': 'Failed to check updates',
    'updates.upgrade': 'Upgrade',
    'updates.upgradingItem': 'Upgrading {id}...',
    'updates.upgradeSuccess': '{id} upgraded successfully',
    'updates.upgradeError': 'Failed to upgrade {id}: {error}',
    'updates.batchComplete': 'Batch upgrade completed',
    'inventory.notLoaded': 'Inventory not loaded',
    'inventory.collecting': 'Collecting inventory data...',
    'inventory.initialLoadingText': 'Fetching local cache and preparing data for display.',
    'inventory.hardware': 'Hardware',
    'inventory.operatingSystem': 'Operating System',
    'inventory.loggedUsers': 'Logged Users',
    'inventory.volumes': 'Volumes (logical_drives)',
    'inventory.networkInterfaces': 'Network Interfaces',
    'inventory.printers': 'Printers',
    'inventory.memoryModules': 'Memory (Modules)',
    'inventory.monitors': 'Monitors',
    'inventory.gpu': 'GPU',
    'inventory.battery': 'Battery',
    'inventory.bitlocker': 'BitLocker',
    'inventory.cpuInfo': 'CPU Info',
    'inventory.startupItems': 'Startup Items',
    'inventory.refreshStartupItems': 'Refresh startup items',
    'inventory.searchStartupPlaceholder': 'Search startup item by name, path, user or source',
    'inventory.autoexec': 'Autoexec',
    'inventory.installedSoftware': 'Installed Software',
    'inventory.refreshSoftware': 'Refresh software',
    'inventory.searchSoftwarePlaceholder': 'Search software by name, version or publisher',
    'inventory.networkConnections': 'Network Connections',
    'inventory.listeningPorts': 'Listening Ports',
    'inventory.openConnections': 'Open Connections',
    'inventory.refreshConnections': 'Refresh ports and connections',
    'inventory.refreshListeningPorts': 'Refresh listening ports',
    'inventory.searchConnectionsPlaceholder': 'Search by process, port, address...',
    'inventory.noneSoftwareFound': 'No software found.',
    'inventory.noneStartupFound': 'No startup item found.',
    'inventory.noneListeningPortFound': 'No port found.',
    'inventory.noneOpenConnectionFound': 'No connection found.',
    'inventory.visibleTotal': 'Visible total: {visible}',
    'inventory.visibleInventoryTotal': 'Visible total: {visible} | Inventory total: {total}',
    'inventory.collectingNow': 'Collecting inventory...',
    'inventory.collectedAt': 'Collected at {date}',
    'inventory.updated': 'Inventory updated.',
    'inventory.collectFailed': 'Failed to collect inventory',
    'inventory.initialCollectFailed': 'Failed to collect the initial data. Click Collect Inventory to try again.',
    'inventory.exportPdfRunningFeedback': 'Exporting inventory to PDF...',
    'inventory.exportPdfRunningStatus': 'PDF export in progress...',
    'inventory.exportNoFile': 'No file returned from the server',
    'inventory.exportNoPath': 'Failed: no path returned',
    'inventory.exportPdfSuccessFeedback': 'PDF exported: {path}',
    'inventory.exportPdfSuccessStatus': 'PDF created successfully at: {path}',
    'inventory.exportPdfError': 'Failed to export PDF: {error}',
    'inventory.osqueryInstalled': 'osquery: installed ({path})',
    'inventory.osqueryNotDetected': 'osquery: not detected (package: {packageId})',
    'inventory.osqueryCheckError': 'osquery: error while checking ({error})',
    'inventory.installingOsquery': 'Installing osquery via winget...',
    'inventory.osqueryInstalledFeedback': 'osquery installation completed.',
    'support.title': 'Support',
    'support.subtitle': 'Tickets linked to this agent',
    'support.myTickets': 'My Tickets',
    'support.noTickets': 'No tickets.',
    'support.closeTicketTitle': 'Close Ticket',
    'support.noRating': 'No rating',
    'support.finalStateOptional': 'Final state (optional)',
    'support.closeWithDefaultState': 'Close with default state',
    'support.customFinalStateGuidPlaceholder': 'GUID of custom final state',
    'support.closingCommentOptional': 'Closing comment (optional)',
    'support.describeAppliedSolution': 'Describe the applied solution...',
    'support.comments': 'Comments',
    'support.writeCommentPlaceholder': 'Write a comment...',
    'support.openTicketTitle': 'Open Ticket',
    'support.ticketTitle': 'Title *',
    'support.problemSummary': 'Problem summary',
    'support.select': 'Select...',
    'support.category.hardware': 'Hardware',
    'support.category.software': 'Software',
    'support.category.network': 'Network',
    'support.category.access': 'Access / Permissions',
    'support.category.email': 'Email',
    'support.category.printer': 'Printer',
    'support.category.vpn': 'VPN',
    'support.category.other': 'Other',
    'support.priority.low': 'Low',
    'support.priority.medium': 'Medium',
    'support.priority.high': 'High',
    'support.priority.critical': 'Critical',
    'support.descriptionRequired': 'Description *',
    'support.describeProblemDetails': 'Describe the problem in detail...',
    'support.fillTitleDescription': 'Fill in title and description',
    'support.sending': 'Sending...',
    'support.submittingTicket': 'Submitting ticket...',
    'support.ticketCreatedSuccess': 'Ticket created successfully!',
    'support.ticketCreateError': 'Failed to create ticket: {error}',
    'support.ticketListLoadError': 'Failed to load tickets: {error}',
    'support.enterComment': 'Enter a comment',
    'support.commentSent': 'Comment sent',
    'support.commentSendError': 'Failed to send comment: {error}',
    'support.invalidRating': 'Invalid rating. Provide a value from 0 to 5.',
    'support.closing': 'Closing...',
    'support.ticketClosedSuccess': 'Ticket closed successfully',
    'support.ticketCloseError': 'Failed to close ticket: {error}',
    'support.agentLabel': 'Agent: {name}',
    'support.serverNotConfigured': 'Server not configured. Configure it in Debug.',
    'support.noTicketsPrompt': 'No tickets at the moment. Click "+ New Ticket" to open one.',
    'support.defaultOpenStatus': 'Open',
    'support.ticketOpenedAt': 'Opened at: {date}',
    'support.ratingDisplay': 'Rating: {rating}',
    'support.ratedAt': 'Rated at: {date}',
    'support.ratedBy': 'Rated by: {name}',
    'support.loadingComments': 'Loading comments...',
    'support.noComments': 'No comments.',
    'support.commentLoadError': 'Failed to load comments: {error}',
    'support.user': 'User',
    'support.internal': 'Internal',
    'knowledge.title': 'Knowledge Base',
    'knowledge.subtitle': 'PC maintenance and troubleshooting articles',
    'knowledge.searchPlaceholder': 'Search by title, category, tag or content',
    'knowledge.loadingArticles': 'Loading articles...',
    'knowledge.article': 'Article',
    'knowledge.noArticlesFound': 'No articles found.',
    'knowledge.loadError': 'Failed to load knowledge base.',
    'automation.title': 'Automation',
    'automation.subtitle': 'Local policy sync and tasks applied to the agent',
    'automation.includeScriptContent': 'Include script content in manual sync',
    'automation.notes': 'Notes',
    'automation.noSyncYet': 'No sync executed yet.',
    'automation.localExecution': 'Local Execution',
    'automation.pendingCallbacks': '{count} pending callbacks',
    'automation.ackResultMessage': 'ACK and RESULT are sent immediately when possible and enter a persistent queue when the backend is unavailable.',
    'automation.resolvedTasks': 'Resolved Tasks',
    'automation.zeroTasks': '{count} tasks',
    'automation.noPolicyLoaded': 'No policy loaded.',
    'automation.recentExecutions': 'Recent Executions',
    'automation.zeroExecutions': '{count} executions',
    'automation.noExecutions': 'No executions recorded.',
    'automation.awaitingConfig': 'Waiting for configuration',
    'automation.synchronized': 'Synchronized',
    'automation.noCommunication': 'No communication',
    'automation.noChanges': 'No changes',
    'automation.newOrLocalPolicy': 'New or local policy',
    'automation.connection': 'Connection',
    'automation.tasks': 'Tasks',
    'automation.pendingCallbacksLabel': 'Pending callbacks',
    'automation.upToDate': 'UpToDate',
    'automation.lastSync': 'Last sync',
    'automation.lastAttempt': 'Last attempt',
    'automation.localCache': 'Local cache',
    'automation.generatedAt': 'Policy generated at {date}',
    'automation.manualSyncWithScript': 'Manual sync with script content enabled',
    'automation.lastErrorLine': 'Last error: {error}',
    'automation.noAlerts': 'No alerts at the moment.',
    'automation.noneResolvedTasks': 'No tasks resolved for the agent.',
    'automation.action': 'Action',
    'automation.scope': 'Scope',
    'automation.updatedAt': 'Updated at',
    'automation.untitledTask': 'Untitled task',
    'automation.viewLogs': 'View logs',
    'automation.hideLogs': 'Hide logs',
    'automation.pendingCallbackChip': 'Pending callback',
    'automation.executionTitle': 'Execution',
    'automation.summary': 'Summary',
    'automation.errorLabel': 'Error',
    'automation.start': 'Start',
    'automation.end': 'End',
    'automation.inProgress': 'In progress',
    'automation.duration': 'Duration',
    'automation.loadingState': 'Loading automation state...',
    'automation.waitingServerConfig': 'Automation is waiting for server/token configuration.',
    'automation.lastErrorState': 'Automation has a last recorded error.',
    'automation.policyLoadedSuccess': 'Policy loaded successfully.',
    'automation.policyLoadedLocal': 'Local policy loaded with no current backend communication.',
    'automation.loadFailed': 'Failed to load automation: {error}',
    'automation.syncingPolicy': 'Syncing policy...',
    'automation.syncWarning': 'Policy synced with warning.',
    'automation.syncSuccess': 'Policy synced successfully.',
    'automation.syncFailed': 'Policy sync failed: {error}',
    'debug.subtitle': 'Debug - remote server connection settings',
    'debug.agentStatusTitle': 'Agent Status',
    'debug.connectionSettings': 'Connection Settings',
    'debug.apiServer': 'API Server (HTTP/HTTPS)',
    'debug.authToken': 'Authentication Token (API)',
    'debug.natsServer': 'NATS Server (Commands)',
    'debug.agentId': 'Agent ID (NATS)',
    'debug.p2pFirstInstall': 'Automation: install via P2P-first (test)',
    'debug.p2pFalseOption': 'false (use Winget CLI)',
    'debug.p2pTrueOption': 'true (try P2P and fallback to Winget)',
    'debug.testResponse': 'Connection test response',
    'debug.offlineDisconnected': 'Offline / Disconnected',
    'debug.serverLabel': 'server: {server}',
    'debug.statusLoadError': 'Failed to load status: {error}',
    'debug.saving': 'Saving...',
    'debug.savedSuccess': 'Configuration saved successfully.',
    'debug.saveError': 'Failed to save: {error}',
    'debug.testingConnection': 'Testing connection...',
    'debug.connectedSuccess': 'Connected successfully.',
    'debug.connectionFailure': 'Connection failed: {error}',
    'chat.subtitle': 'AI Chat - talk to the AI to manage your computer',
    'chat.memories': 'Memories',
    'chat.apiServerOptional': 'API Server (optional)',
    'chat.agentTokenOptional': 'Agent Token (optional)',
    'chat.modelCompatibility': 'Model (compatibility)',
    'chat.maxTokensOptional': 'Max Tokens (optional)',
    'chat.assistantInstructionsOptional': 'Assistant Instructions (Optional)',
    'chat.systemPromptPlaceholder': 'Example: Answer with a focus on corporate inventory and always cite risks before executing actions.',
    'chat.configUsesDebugFallback': 'If endpoint/token are empty, chat automatically uses apiScheme/apiServer/authToken from the Debug tab.',
    'chat.inputPlaceholder': 'Type your message... (Enter to send, Shift+Enter for a new line)',
    'chat.logsTitle': 'AI Chat Logs',
    'chat.localMemories': 'Local Memories',
    'chat.noMemoryFound': 'No memory found.',
    'chat.stopping': 'Stopping...',
    'chat.thinking': 'Thinking...',
    'chat.responseInterrupted': '_Response interrupted by the user._',
    'chat.errorUnknown': 'Error: {error}',
    'chat.maxTokensValidation': 'Max Tokens must be 0 or greater',
    'chat.configSavedSuccess': 'AI configuration saved successfully',
    'chat.configSaveError': 'Failed to save configuration: {error}',
    'chat.configTesting': 'Testing AI configuration...',
    'chat.configTestSuccess': 'Test completed successfully{suffix}',
    'chat.configTestFailure': 'Configuration test failed: {error}',
    'chat.toolsLoadError': 'Failed to load tools',
    'chat.noLogsYet': '(no chat logs yet)',
    'chat.logsLoadError': 'Failed to load logs: {error}',
    'chat.updatedAt': '(updated: {date})',
    'chat.memoriesLoadError': 'Failed to load memories: {error}',
    'chat.memoryDeleteError': 'Failed to delete memories: {error}',
    'chat.cleared': 'Chat cleared',
    'chat.clearError': 'Error: {error}',
    'action.delete': 'Delete',
  },
};

var appLocaleInitPromise = null;
var appTranslationObserver = null;

function normalizeAppLocale(raw) {
  var value = String(raw || '').trim().replace(/_/g, '-').toLowerCase();
  if (!value) return 'pt-br';
  if (value.indexOf('en') === 0) return 'en-us';
  if (value.indexOf('pt') === 0) return 'pt-br';
  return 'pt-br';
}

function getAppLocaleTag(locale) {
  return APP_LOCALE_TAGS[normalizeAppLocale(locale)] || 'pt-BR';
}

function getAppLocale() {
  var root = typeof document !== 'undefined' ? document.documentElement : null;
  return normalizeAppLocale(root && root.lang ? root.lang : 'pt-BR');
}

function setDocumentLocale(localeRaw) {
  var normalized = normalizeAppLocale(localeRaw);
  if (typeof document !== 'undefined' && document.documentElement) {
    document.documentElement.lang = getAppLocaleTag(normalized);
  }
  return normalized;
}

function detectClientLocale() {
  var candidates = [];
  if (typeof navigator !== 'undefined') {
    if (Array.isArray(navigator.languages)) {
      candidates = candidates.concat(navigator.languages);
    }
    candidates.push(navigator.language);
    candidates.push(navigator.userLanguage);
  }
  if (typeof document !== 'undefined' && document.documentElement) {
    candidates.push(document.documentElement.lang);
  }

  for (var i = 0; i < candidates.length; i += 1) {
    var normalized = normalizeAppLocale(candidates[i]);
    if (APP_I18N_DICTIONARY[normalized]) {
      return normalized;
    }
  }
  return 'pt-br';
}

function translate(key, replacements) {
  var locale = getAppLocale();
  var dict = APP_I18N_DICTIONARY[locale] || APP_I18N_DICTIONARY['pt-br'];
  var template = dict[key];
  if (template === undefined) {
    template = APP_I18N_DICTIONARY['pt-br'][key];
  }
  if (template === undefined) {
    return String(key || '');
  }
  return String(template).replace(/\{([a-zA-Z0-9_]+)\}/g, function (_match, token) {
    if (!replacements || replacements[token] === undefined || replacements[token] === null) {
      return '';
    }
    return String(replacements[token]);
  });
}

function applyTranslations(root) {
  var scope = root || document;
  if (!scope || !scope.querySelectorAll) return;
  var localeTag = getAppLocaleTag(getAppLocale());

  if (scope.nodeType === 1 && scope.hasAttribute && scope.hasAttribute('data-app-lang')) {
    scope.setAttribute('lang', localeTag);
  }

  var localeNodes = scope.querySelectorAll('[data-app-lang]');
  for (var n = 0; n < localeNodes.length; n += 1) {
    localeNodes[n].setAttribute('lang', localeTag);
  }

  var textNodes = scope.querySelectorAll('[data-i18n]');
  for (var i = 0; i < textNodes.length; i += 1) {
    var textEl = textNodes[i];
    textEl.textContent = translate(textEl.getAttribute('data-i18n'));
  }

  var titleNodes = scope.querySelectorAll('[data-i18n-title]');
  for (var j = 0; j < titleNodes.length; j += 1) {
    var titleEl = titleNodes[j];
    titleEl.setAttribute('title', translate(titleEl.getAttribute('data-i18n-title')));
  }

  var ariaNodes = scope.querySelectorAll('[data-i18n-aria-label]');
  for (var k = 0; k < ariaNodes.length; k += 1) {
    var ariaEl = ariaNodes[k];
    ariaEl.setAttribute('aria-label', translate(ariaEl.getAttribute('data-i18n-aria-label')));
  }

  var placeholderNodes = scope.querySelectorAll('[data-i18n-placeholder]');
  for (var m = 0; m < placeholderNodes.length; m += 1) {
    var placeholderEl = placeholderNodes[m];
    placeholderEl.setAttribute('placeholder', translate(placeholderEl.getAttribute('data-i18n-placeholder')));
  }
}

function ensureTranslationObserver() {
  if (appTranslationObserver || typeof MutationObserver === 'undefined' || !document || !document.body) {
    return;
  }

  appTranslationObserver = new MutationObserver(function (mutations) {
    for (var i = 0; i < mutations.length; i += 1) {
      var mutation = mutations[i];
      if (!mutation.addedNodes || !mutation.addedNodes.length) continue;
      for (var j = 0; j < mutation.addedNodes.length; j += 1) {
        var node = mutation.addedNodes[j];
        if (!node || node.nodeType !== 1) continue;
        var hasI18nAttrs = node.matches && node.matches('[data-i18n], [data-i18n-title], [data-i18n-aria-label], [data-i18n-placeholder]');
        var hasI18nDescendants = node.querySelector && node.querySelector('[data-i18n], [data-i18n-title], [data-i18n-aria-label], [data-i18n-placeholder]');
        if (hasI18nAttrs || hasI18nDescendants) {
          applyTranslations(node);
        }
      }
    }
  });

  appTranslationObserver.observe(document.body, { childList: true, subtree: true });
}

function initApplicationLocale() {
  if (appLocaleInitPromise) return appLocaleInitPromise;

  appLocaleInitPromise = Promise.resolve().then(function () {
    var fallbackLocale = detectClientLocale();

    if (!(window.go && window.go.app && window.go.app.App && typeof window.go.app.App.GetPreferredLocale === 'function')) {
      setDocumentLocale(fallbackLocale || 'pt-BR');
      applyTranslations(document);
      ensureTranslationObserver();
      return getAppLocaleTag(getAppLocale());
    }

    return window.go.app.App.GetPreferredLocale().then(function (locale) {
      setDocumentLocale(locale || fallbackLocale || 'pt-BR');
      applyTranslations(document);
      ensureTranslationObserver();
      return getAppLocaleTag(getAppLocale());
    }).catch(function () {
      setDocumentLocale(fallbackLocale || 'pt-BR');
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
    timeoutId = setTimeout(function () { fn.apply(ctx, args); }, delayMs);
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
  if (fallback === undefined) fallback = '-';
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
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#039;');
}

function escapeHtmlAttr(value) {
  return escapeHtml(value).replaceAll('`', '');
}

function normalizeKbScope(scope) {
  var v = String(scope || '').trim().toLowerCase();
  if (v === 'global') return 'Global';
  if (v === 'client') return 'Cliente';
  if (v === 'site') return 'Site';
  return scope || '-';
}

function renderInlineMarkdown(text) {
  var html = escapeHtml(text || '');
  html = html.replace(/`([^`]+)`/g, '<code>$1</code>');
  html = html.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
  html = html.replace(/\*([^*]+)\*/g, '<em>$1</em>');
  html = html.replace(/\[([^\]]+)\]\(([^)]+)\)/g, function (_m, label, rawUrl) {
    var href = String(rawUrl || '').trim();
    var safeHref = '#';
    if (/^https?:\/\//i.test(href) || /^mailto:/i.test(href)) {
      safeHref = href;
    }
    return '<a href="' + escapeHtmlAttr(safeHref) + '" target="_blank" rel="noopener noreferrer">' + label + '</a>';
  });
  return html;
}

function splitMarkdownTableRow(line) {
  var raw = String(line || '').trim();
  if (raw.startsWith('|')) raw = raw.slice(1);
  if (raw.endsWith('|')) raw = raw.slice(0, -1);
  return raw.split('|').map(function (c) { return c.trim(); });
}

function isMarkdownTableSeparator(line) {
  var raw = String(line || '').trim();
  if (!raw.includes('|')) return false;
  if (raw.startsWith('|')) raw = raw.slice(1);
  if (raw.endsWith('|')) raw = raw.slice(0, -1);
  var cells = raw.split('|').map(function (c) { return c.trim(); });
  if (!cells.length) return false;
  for (var i = 0; i < cells.length; i++) {
    if (!/^:?-{3,}:?$/.test(cells[i])) return false;
  }
  return true;
}

function getTableAlignments(separatorLine) {
  var raw = String(separatorLine || '').trim();
  if (raw.startsWith('|')) raw = raw.slice(1);
  if (raw.endsWith('|')) raw = raw.slice(0, -1);
  var cells = raw.split('|').map(function (c) { return c.trim(); });
  return cells.map(function (c) {
    var left = c.startsWith(':');
    var right = c.endsWith(':');
    if (left && right) return 'center';
    if (right) return 'right';
    if (left) return 'left';
    return '';
  });
}

function escapeRegExp(text) {
  return String(text).replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

function applySyntaxToken(html, regex, klass) {
  return html.replace(regex, function (m) {
    return '<span class="' + klass + '">' + m + '</span>';
  });
}

function highlightCodeBasic(code, lang) {
  var normalizedLang = String(lang || '').trim().toLowerCase();
  var html = escapeHtml(code || '');

  html = applySyntaxToken(html, /("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*')/g, 'kb-syntax-str');
  html = applySyntaxToken(html, /\b\d+(?:\.\d+)?\b/g, 'kb-syntax-num');

  if (normalizedLang === 'json') {
    html = applySyntaxToken(html, /"([^"\\]|\\.)*"(?=\s*:)/g, 'kb-syntax-key');
    html = applySyntaxToken(html, /\b(true|false|null)\b/g, 'kb-syntax-kw');
    return html;
  }

  if (normalizedLang === 'bash' || normalizedLang === 'sh' || normalizedLang === 'powershell' || normalizedLang === 'ps1') {
    html = applySyntaxToken(html, /#[^\n]*/g, 'kb-syntax-com');
    html = applySyntaxToken(html, /\b(if|then|else|fi|for|do|done|while|case|esac|function|return|exit)\b/g, 'kb-syntax-kw');
    return html;
  }

  html = applySyntaxToken(html, /\/\/[^\n]*/g, 'kb-syntax-com');
  html = applySyntaxToken(html, /\b(func|function|return|if|else|switch|case|default|for|range|break|continue|const|let|var|type|struct|interface|map|chan|package|import|try|catch|throw|new|class|public|private|protected|static|async|await|nil|true|false)\b/g, 'kb-syntax-kw');

  if (normalizedLang === 'go') {
    var goTypes = ['string', 'int', 'int64', 'float64', 'bool', 'byte', 'rune', 'error', 'any'];
    var goPattern = new RegExp('\\b(' + goTypes.map(escapeRegExp).join('|') + ')\\b', 'g');
    html = applySyntaxToken(html, goPattern, 'kb-syntax-typ');
  }

  return html;
}

function renderMarkdown(markdown) {
  var lines = String(markdown || '').replace(/\r\n/g, '\n').split('\n');
  var html = [];
  var inCode = false;
  var inCodeLang = '';
  var codeLines = [];
  var paragraph = [];
  var listType = '';

  function flushParagraph() {
    if (!paragraph.length) return;
    html.push('<p>' + renderInlineMarkdown(paragraph.join(' ')) + '</p>');
    paragraph = [];
  }

  function closeList() {
    if (!listType) return;
    html.push('</' + listType + '>');
    listType = '';
  }

  function renderCodeBlock(lines, fenceLang) {
    var lang = String(fenceLang || '').trim();
    var className = lang ? ' class="language-' + escapeHtmlAttr(lang.toLowerCase()) + '"' : '';
    var highlighted = highlightCodeBasic(lines.join('\n'), lang);
    return '<pre><code' + className + '>' + highlighted + '</code></pre>';
  }

  for (var i = 0; i < lines.length; i++) {
    var line = lines[i];
    var trimmed = line.trim();

    if (trimmed.startsWith('```')) {
      flushParagraph();
      closeList();
      if (!inCode) {
        inCode = true;
        var fenceInfo = trimmed.slice(3).trim();
        inCodeLang = fenceInfo ? fenceInfo.split(/\s+/)[0] : '';
        codeLines = [];
      } else {
        html.push(renderCodeBlock(codeLines, inCodeLang));
        inCode = false;
        inCodeLang = '';
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
      html.push('<h' + level + '>' + renderInlineMarkdown(heading[2]) + '</h' + level + '>');
      continue;
    }

    if (trimmed.includes('|') && i + 1 < lines.length && isMarkdownTableSeparator(lines[i + 1])) {
      flushParagraph();
      closeList();

      var headers = splitMarkdownTableRow(trimmed);
      var alignments = getTableAlignments(lines[i + 1]);
      html.push('<div class="kb-table-wrap"><table class="kb-markdown-table"><thead><tr>');
      for (var h = 0; h < headers.length; h++) {
        var hAlign = alignments[h] ? ' style="text-align:' + alignments[h] + '"' : '';
        html.push('<th' + hAlign + '>' + renderInlineMarkdown(headers[h]) + '</th>');
      }
      html.push('</tr></thead><tbody>');

      i += 2;
      for (; i < lines.length; i++) {
        var rowTrimmed = String(lines[i] || '').trim();
        if (!rowTrimmed || !rowTrimmed.includes('|')) {
          i -= 1;
          break;
        }
        var rowCells = splitMarkdownTableRow(rowTrimmed);
        html.push('<tr>');
        for (var c = 0; c < headers.length; c++) {
          var cell = c < rowCells.length ? rowCells[c] : '';
          var align = alignments[c] ? ' style="text-align:' + alignments[c] + '"' : '';
          html.push('<td' + align + '>' + renderInlineMarkdown(cell) + '</td>');
        }
        html.push('</tr>');
      }
      html.push('</tbody></table></div>');
      continue;
    }

    var unordered = trimmed.match(/^[-*]\s+(.+)$/);
    if (unordered) {
      flushParagraph();
      if (listType !== 'ul') {
        closeList();
        listType = 'ul';
        html.push('<ul>');
      }
      html.push('<li>' + renderInlineMarkdown(unordered[1]) + '</li>');
      continue;
    }

    var ordered = trimmed.match(/^\d+\.\s+(.+)$/);
    if (ordered) {
      flushParagraph();
      if (listType !== 'ol') {
        closeList();
        listType = 'ol';
        html.push('<ol>');
      }
      html.push('<li>' + renderInlineMarkdown(ordered[1]) + '</li>');
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

  return html.join('');
}

function buildKnowledgeMeta(article) {
  return [
    '<span>' + escapeHtml(article.id || '-') + '</span>',
    '<span>' + escapeHtml(article.category || '-') + '</span>',
    '<span>Escopo: ' + escapeHtml(normalizeKbScope(article.scope)) + '</span>',
    '<span>Autor: ' + escapeHtml(article.author || '-') + '</span>',
    '<span>Nivel: ' + escapeHtml(article.difficulty || '-') + '</span>',
    '<span>Leitura: ' + escapeHtml(String(article.readTimeMin || '-')) + ' min</span>',
    '<span>Publicado: ' + escapeHtml(article.publishedAt || '-') + '</span>',
    '<span>Atualizado: ' + escapeHtml(article.updatedAt || '-') + '</span>'
  ].join('');
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
  if (usage === null) return '';
  return '<div class="disk-bar"><span style="width: ' + escapeHtmlAttr(String(usage)) + '%"></span></div>';
}

function renderDiskUsageLabel(disk) {
  var usage = getDiskUsagePercent(disk);
  if (usage === null) return 'Uso: indisponivel';
  var freePct = 100 - usage;
  return 'Uso: ' + usage + '% | Livre: ' + freePct + '%';
}

function renderDiskOccupiedGB(disk) {
  if (!disk.freeKnown) return 'indisponivel';
  var size = Number(disk.sizeGB || 0);
  var free = Number(disk.freeGB || 0);
  if (!Number.isFinite(size) || !Number.isFinite(free) || size <= 0) return 'indisponivel';
  var occupied = Math.max(0, size - free);
  return occupied.toFixed(2) + ' GB';
}
