package mcp

import (
	"context"
	"discovery/app/core/database"
	"encoding/json"
	"fmt"
	"strings"
)

// AppBridge is the interface the tool registration needs from the App layer.
// It avoids a circular dependency with the main package.
type AppBridge interface {
	GetInventoryJSON() (json.RawMessage, error)
	SearchCatalog(query string) (json.RawMessage, error)
	InstallPackage(id string) (string, error)
	UninstallPackage(id string) (string, error)
	UpgradePackage(id string) (string, error)
	UpgradeAllPackages() (string, error)
	GetPendingUpdatesJSON() (json.RawMessage, error)
	GetPackageActionsJSON() (json.RawMessage, error)
	ExportMarkdown() (string, error)
	ExportPDF() (string, error)
	GetOsqueryStatusJSON() (json.RawMessage, error)
	ListPrintersJSON() (json.RawMessage, error)
	InstallPrinterJSON(name, driverName, portName, portAddress string) (json.RawMessage, error)
	InstallSharedPrinterJSON(connectionPath string, setDefault bool) (json.RawMessage, error)
	RemovePrinterJSON(name string) (json.RawMessage, error)
	GetPrinterConfigJSON(name string) (json.RawMessage, error)
	ListPrintJobsJSON(printerName string) (json.RawMessage, error)
	RemovePrintJobJSON(printerName string, jobID int) (json.RawMessage, error)
	GetSpoolerStatusJSON() (json.RawMessage, error)
	RestartSpoolerJSON() (json.RawMessage, error)
	ClearPrintQueueJSON(printerName string) (json.RawMessage, error)
	ListPrinterDriversJSON() (json.RawMessage, error)
	ListInstalled() (string, error)
	GetLogsText() string

	// Memorias locais (notas)
	GetLocalMemories() ([]database.MemoryNote, error)
	AddLocalMemory(content string) (database.MemoryNote, error)
	DeleteLocalMemory(id int64) error

	// Tickets
	GetAgentInfoJSON() (json.RawMessage, error)
	ListAgentTickets() (json.RawMessage, error)
	GetAgentTicketDetails(ticketID string) (json.RawMessage, error)
	AddAgentTicketComment(ticketID, content string) (json.RawMessage, error)
	CreateAgentTicket(title, description string, priority int, category, templateID, departmentID, customFieldsJSON, templateAnswersJSON string) (json.RawMessage, error)
	CloseAgentTicket(ticketID string, rating *int, comment, workflowStateID string) (json.RawMessage, error)
	ReopenAgentTicket(ticketID, reason string) (json.RawMessage, error)
	RateAgentTicket(ticketID string, rating int, feedback string) (json.RawMessage, error)
	ListAgentTicketTemplates() (json.RawMessage, error)
	ListTicketDepartmentsJSON() (json.RawMessage, error)
	ListDepartmentFieldsJSON(departmentID string) (json.RawMessage, error)

	// Chat — pergunta interativa ao usuario
	AskUserChat(question, optionsJSON, allowText string) (string, error)
	// AskUserChatWithContext é a variante consciente de contexto: o
	// cancelamento do stream de chat (botão Parar) interrompe a espera pela
	// resposta do usuário em vez de deixar a goroutine pendurada.
	AskUserChatWithContext(ctx context.Context, question, optionsJSON, allowText string) (string, error)

	// Captura de tela — visão do LLM (PLANO_CAPTURA_TELA_ASSISTIDA).
	// ListOpenWindowsJSON: janelas visíveis + monitores (não captura nada).
	// includeUntitled permite listar janelas sem título (games/UWP).
	// CaptureScreenshotForTool: captura com autorização do usuário e devolve
	// a imagem no contrato {"image_base64","mime"} consumido pela API.
	// ScreenshotConsentStatusJSON: estado da autorização + auditoria.
	ListOpenWindowsJSON(includeUntitled bool) (json.RawMessage, error)
	CaptureScreenshotForTool(ctx context.Context, args map[string]any) (json.RawMessage, error)
	ScreenshotConsentStatusJSON() (json.RawMessage, error)
}

// RegisterDiscoveryTools adds all Discovery app tools to the registry.
func RegisterDiscoveryTools(reg *Registry, app AppBridge) {
	// ========== INVENTARIO ==========
	reg.Register(Tool{
		Name:        "get_inventory",
		Description: "Retorna o inventario completo do computador: hardware, SO, discos, rede, usuarios logados, bateria, CPU, GPU, memoria, BitLocker, software instalado, startup items.",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return app.GetInventoryJSON()
		},
	})

	reg.Register(Tool{
		Name:        "export_inventory_markdown",
		Description: "Exporta o relatorio de inventario em formato Markdown e retorna o caminho do arquivo gerado.",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			path, err := app.ExportMarkdown()
			return map[string]string{"path": path}, err
		},
	})

	reg.Register(Tool{
		Name:        "export_inventory_pdf",
		Description: "Exporta o relatorio de inventario em formato PDF e retorna o caminho do arquivo gerado.",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			path, err := app.ExportPDF()
			return map[string]string{"path": path}, err
		},
	})

	// ========== BUSCA E INSTALACAO ==========
	reg.Register(Tool{
		Name:        "search_packages",
		Description: "Pesquisa pacotes no catalogo winget por nome, ID ou publisher. Retorna ate 20 resultados.",
		Params: []ToolParam{
			{Name: "query", Type: "string", Description: "Termo de busca (nome, ID ou publisher do pacote)", Required: true},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			q, _ := args["query"].(string)
			if strings.TrimSpace(q) == "" {
				return nil, fmt.Errorf("query nao pode ser vazia")
			}
			return app.SearchCatalog(q)
		},
	})

	reg.Register(Tool{
		Name:        "install_package",
		Description: "Instala um pacote via winget pelo seu ID (ex: 'Google.Chrome', 'Mozilla.Firefox').",
		Params: []ToolParam{
			{Name: "id", Type: "string", Description: "ID do pacote winget (ex: Google.Chrome)", Required: true},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			id, _ := args["id"].(string)
			if strings.TrimSpace(id) == "" {
				return nil, fmt.Errorf("id do pacote nao pode ser vazio")
			}
			out, err := app.InstallPackage(id)
			return map[string]string{"output": out}, err
		},
	})

	// ========== GERENCIAMENTO DE PACOTES INSTALADOS ==========
	reg.Register(Tool{
		Name:        "list_installed_packages",
		Description: "Lista todos os pacotes (programas) atualmente instalados na maquina, detectados pelo winget.",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			out, err := app.ListInstalled()
			return map[string]string{"output": out}, err
		},
	})

	reg.Register(Tool{
		Name:        "uninstall_package",
		Description: "Desinstala um pacote via winget pelo seu ID. DESTRUTIVA: exige confirm=true (obtido do usuario).",
		Params: []ToolParam{
			{Name: "id", Type: "string", Description: "ID do pacote winget", Required: true},
			{Name: "confirm", Type: "boolean", Description: "Confirmação explícita do usuário (M31)", Required: true},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			if err := requireConfirm(args); err != nil {
				return nil, err
			}
			id, _ := args["id"].(string)
			if strings.TrimSpace(id) == "" {
				return nil, fmt.Errorf("id do pacote nao pode ser vazio")
			}
			out, err := app.UninstallPackage(id)
			return map[string]string{"output": out}, err
		},
	})

	reg.Register(Tool{
		Name:        "upgrade_package",
		Description: "Atualiza um pacote especifico via winget.",
		Params: []ToolParam{
			{Name: "id", Type: "string", Description: "ID do pacote winget", Required: true},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			id, _ := args["id"].(string)
			if strings.TrimSpace(id) == "" {
				return nil, fmt.Errorf("id do pacote nao pode ser vazio")
			}
			out, err := app.UpgradePackage(id)
			return map[string]string{"output": out}, err
		},
	})

	reg.Register(Tool{
		Name:        "get_pending_updates",
		Description: "Lista todos os pacotes que possuem atualizacoes disponiveis, com versao atual e versao disponivel.",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return app.GetPendingUpdatesJSON()
		},
	})

	reg.Register(Tool{
		Name:        "get_package_actions",
		Description: "Retorna o mapa de acao contextual por pacote (install, uninstall, upgrade).",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return app.GetPackageActionsJSON()
		},
	})

	reg.Register(Tool{
		Name:        "upgrade_all_packages",
		Description: "Atualiza todos os pacotes que possuem atualizacao disponivel via winget. DESTRUTIVA em massa: exige confirm=true.",
		Params: []ToolParam{
			{Name: "confirm", Type: "boolean", Description: "Confirmação explícita do usuário (M31)", Required: true},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			if err := requireConfirm(args); err != nil {
				return nil, err
			}
			out, err := app.UpgradeAllPackages()
			return map[string]string{"output": out}, err
		},
	})

	// ========== SISTEMA E DIAGNOSTICOS ==========
	reg.Register(Tool{
		Name:        "get_osquery_status",
		Description: "Verifica se o osquery esta instalado no computador e retorna o caminho do binario.",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return app.GetOsqueryStatusJSON()
		},
	})

	// ========== MEMORIAS LOCAIS ==========
	reg.Register(Tool{
		Name:        "memory/list",
		Description: "Lista as memorias/anotacoes locais gravadas pelo agente.",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return app.GetLocalMemories()
		},
	})

	reg.Register(Tool{
		Name:        "memory/create",
		Description: "Cria uma nova memorias/anotacao local.",
		Params: []ToolParam{
			{Name: "content", Type: "string", Description: "Conteudo da anotacao", Required: true},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			content, err := requiredStringArg(args, "content")
			if err != nil {
				return nil, err
			}
			return app.AddLocalMemory(content)
		},
	})

	reg.Register(Tool{
		Name:        "memory/delete",
		Description: "Remove uma memorias/anotacao local pelo ID.",
		Params: []ToolParam{
			{Name: "id", Type: "integer", Description: "ID da anotacao", Required: true},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			id, err := requiredIntArg(args, "id")
			if err != nil {
				return nil, err
			}
			return map[string]bool{"ok": true}, app.DeleteLocalMemory(int64(id))
		},
	})

	// ========== IMPRESSORAS ==========
	reg.Register(Tool{
		Name:        "list_printers",
		Description: "Lista as impressoras instaladas no Windows com driver, porta e status.",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return app.ListPrintersJSON()
		},
	})

	reg.Register(Tool{
		Name:        "install_printer",
		Description: "Instala uma impressora usando nome, driver, porta e opcionalmente um endereco para criar a porta TCP/IP.",
		Params: []ToolParam{
			{Name: "name", Type: "string", Description: "Nome da impressora", Required: true},
			{Name: "driverName", Type: "string", Description: "Nome do driver de impressao", Required: true},
			{Name: "portName", Type: "string", Description: "Nome da porta local ou TCP/IP", Required: true},
			{Name: "portAddress", Type: "string", Description: "IP ou hostname para criar a porta, se ela ainda nao existir", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			name, err := requiredStringArg(args, "name")
			if err != nil {
				return nil, err
			}
			driverName, err := requiredStringArg(args, "driverName")
			if err != nil {
				return nil, err
			}
			portName, err := requiredStringArg(args, "portName")
			if err != nil {
				return nil, err
			}
			portAddress := optionalStringArg(args, "portAddress")
			return app.InstallPrinterJSON(name, driverName, portName, portAddress)
		},
	})

	reg.Register(Tool{
		Name:        "install_shared_printer",
		Description: "Instala uma impressora compartilhada via caminho UNC, por exemplo \\\\servidor\\fila.",
		Params: []ToolParam{
			{Name: "connectionPath", Type: "string", Description: "Caminho UNC da impressora compartilhada (ex: \\\\servidor\\impressora)", Required: true},
			{Name: "setDefault", Type: "boolean", Description: "Se true, define a impressora instalada como padrao", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			connectionPath, err := requiredStringArg(args, "connectionPath")
			if err != nil {
				return nil, err
			}
			setDefault := false
			if value, ok := args["setDefault"].(bool); ok {
				setDefault = value
			}
			return app.InstallSharedPrinterJSON(connectionPath, setDefault)
		},
	})

	reg.Register(Tool{
		Name:        "remove_printer",
		Description: "Remove uma impressora instalada pelo nome.",
		Params: []ToolParam{
			{Name: "name", Type: "string", Description: "Nome da impressora", Required: true},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			name, err := requiredStringArg(args, "name")
			if err != nil {
				return nil, err
			}
			return app.RemovePrinterJSON(name)
		},
	})

	reg.Register(Tool{
		Name:        "get_printer_config",
		Description: "Retorna a configuracao atual de impressao para uma impressora especifica.",
		Params: []ToolParam{
			{Name: "printerName", Type: "string", Description: "Nome da impressora", Required: true},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			printerName, err := requiredStringArg(args, "printerName")
			if err != nil {
				return nil, err
			}
			return app.GetPrinterConfigJSON(printerName)
		},
	})

	reg.Register(Tool{
		Name:        "list_print_jobs",
		Description: "Lista os jobs atualmente na fila de uma impressora.",
		Params: []ToolParam{
			{Name: "printerName", Type: "string", Description: "Nome da impressora", Required: true},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			printerName, err := requiredStringArg(args, "printerName")
			if err != nil {
				return nil, err
			}
			return app.ListPrintJobsJSON(printerName)
		},
	})

	reg.Register(Tool{
		Name:        "remove_print_job",
		Description: "Cancela um job especifico da fila de impressao.",
		Params: []ToolParam{
			{Name: "printerName", Type: "string", Description: "Nome da impressora", Required: true},
			{Name: "jobId", Type: "integer", Description: "ID numerico do job de impressao", Required: true},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			printerName, err := requiredStringArg(args, "printerName")
			if err != nil {
				return nil, err
			}
			jobID, err := requiredIntArg(args, "jobId")
			if err != nil {
				return nil, err
			}
			return app.RemovePrintJobJSON(printerName, jobID)
		},
	})

	reg.Register(Tool{
		Name:        "spooler_status",
		Description: "Consulta o status atual do servico Spooler de impressao.",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return app.GetSpoolerStatusJSON()
		},
	})

	reg.Register(Tool{
		Name:        "restart_spooler",
		Description: "Reinicia o servico Spooler de impressao. DESTRUTIVA (para jobs de impressão): exige confirm=true.",
		Params: []ToolParam{
			{Name: "confirm", Type: "boolean", Description: "Confirmação explícita do usuário (M31)", Required: true},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			if err := requireConfirm(args); err != nil {
				return nil, err
			}
			return app.RestartSpoolerJSON()
		},
	})

	reg.Register(Tool{
		Name:        "clear_queue",
		Description: "Limpa todos os jobs pendentes da fila de uma impressora. DESTRUTIVA: exige confirm=true.",
		Params: []ToolParam{
			{Name: "printerName", Type: "string", Description: "Nome da impressora", Required: true},
			{Name: "confirm", Type: "boolean", Description: "Confirmação explícita do usuário (M31)", Required: true},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			if err := requireConfirm(args); err != nil {
				return nil, err
			}
			printerName, err := requiredStringArg(args, "printerName")
			if err != nil {
				return nil, err
			}
			return app.ClearPrintQueueJSON(printerName)
		},
	})

	reg.Register(Tool{
		Name:        "list_drivers",
		Description: "Lista os drivers de impressora instalados no Windows.",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return app.ListPrinterDriversJSON()
		},
	})

	reg.Register(Tool{
		Name:        "get_logs",
		Description: "Retorna os logs recentes de operacoes do winget (instalacao, atualizacao, etc).",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return map[string]string{"logs": app.GetLogsText()}, nil
		},
	})

	reg.Register(Tool{
		Name:        "ping_host",
		Description: "Verifica se um host/IP na rede local esta online usando ping (apenas redes privadas).",
		Params: []ToolParam{
			{Name: "host", Type: "string", Description: "Nome ou IP (privado) a ser verificado", Required: true},
			{Name: "count", Type: "integer", Description: "Numero de pacotes ping (padrao 1)", Required: false},
			{Name: "timeoutSeconds", Type: "integer", Description: "Timeout em segundos (padrao 5)", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			host, err := requiredStringArg(args, "host")
			if err != nil {
				return nil, err
			}
			count := 1
			if v, ok := args["count"]; ok {
				if n, ok := v.(float64); ok {
					count = int(n)
				}
				if n, ok := v.(int); ok {
					count = n
				}
			}
			timeout := 5
			if v, ok := args["timeoutSeconds"]; ok {
				if n, ok := v.(float64); ok {
					timeout = int(n)
				}
				if n, ok := v.(int); ok {
					timeout = n
				}
			}
			return PingHost(ctx, host, count, timeout)
		},
	})

	reg.Register(Tool{
		Name:        "flush_dns",
		Description: "Limpa o cache DNS do sistema (ipconfig /flushdns no Windows).",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return FlushDNS(ctx)
		},
	})

	// ========== NAVEGACAO INTERNA ==========
	reg.Register(Tool{
		Name:        "get_internal_navigation_routes",
		Description: "Lista as rotas internas disponiveis no app para construir links discovery:// clicaveis no chat.",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return []map[string]string{
				{"target": "support_tickets", "url": "discovery://support/tickets", "description": "Abre a tela de chamados"},
				{"target": "support_ticket", "url": "discovery://support/ticket/{ticketId}", "description": "Abre chamado especifico"},
				{"target": "store", "url": "discovery://store", "description": "Abre a aba Loja"},
				{"target": "updates", "url": "discovery://updates", "description": "Abre a aba Atualizacoes"},
				{"target": "inventory", "url": "discovery://inventory", "description": "Abre a aba Inventario"},
				{"target": "logs", "url": "discovery://logs", "description": "Abre a aba Logs"},
				{"target": "chat", "url": "discovery://chat", "description": "Abre a aba Chat IA"},
				{"target": "knowledge", "url": "discovery://knowledge", "description": "Abre a Base de Conhecimento"},
				{"target": "knowledge_article", "url": "discovery://knowledge/article/{articleId}", "description": "Abre um artigo especifico da Base de Conhecimento"},
				{"target": "debug", "url": "discovery://debug", "description": "Abre a aba Debug"},
			}, nil
		},
	})

	reg.Register(Tool{
		Name:        "build_internal_navigation_link",
		Description: "Monta um link interno discovery:// e um markdown de card clicavel para navegação interna no app.",
		Params: []ToolParam{
			{Name: "target", Type: "string", Description: "Destino: support_tickets, support_ticket, store, updates, inventory, logs, chat, knowledge, knowledge_article, debug", Required: true},
			{Name: "ticketId", Type: "string", Description: "GUID do chamado (obrigatorio apenas para target=support_ticket)", Required: false},
			{Name: "articleId", Type: "string", Description: "GUID do artigo da Base de Conhecimento (obrigatorio apenas para target=knowledge_article)", Required: false},
			{Name: "title", Type: "string", Description: "Titulo do card/botao", Required: false},
			{Name: "subtitle", Type: "string", Description: "Subtitulo do card", Required: false},
			{Name: "meta", Type: "string", Description: "Meta adicional do card", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			target, _ := args["target"].(string)
			target = strings.TrimSpace(strings.ToLower(target))
			if target == "" {
				return nil, fmt.Errorf("target nao pode ser vazio")
			}

			ticketID, _ := args["ticketId"].(string)
			ticketID = strings.TrimSpace(ticketID)

			articleID, _ := args["articleId"].(string)
			articleID = strings.TrimSpace(articleID)

			urlByTarget := map[string]string{
				"support_tickets": "discovery://support/tickets",
				"store":           "discovery://store",
				"updates":         "discovery://updates",
				"inventory":       "discovery://inventory",
				"logs":            "discovery://logs",
				"chat":            "discovery://chat",
				"knowledge":       "discovery://knowledge",
				"debug":           "discovery://debug",
			}

			var url string
			if target == "support_ticket" {
				if ticketID == "" {
					return nil, fmt.Errorf("ticketId e obrigatorio para target=support_ticket")
				}
				url = "discovery://support/ticket/" + ticketID
			} else if target == "knowledge_article" {
				if articleID == "" {
					return nil, fmt.Errorf("articleId e obrigatorio para target=knowledge_article")
				}
				url = "discovery://knowledge/article/" + articleID
			} else {
				u, ok := urlByTarget[target]
				if !ok {
					return nil, fmt.Errorf("target invalido: %s", target)
				}
				url = u
			}

			title, _ := args["title"].(string)
			title = strings.TrimSpace(title)
			if title == "" {
				title = "Abrir"
			}

			subtitle, _ := args["subtitle"].(string)
			subtitle = strings.TrimSpace(subtitle)
			if subtitle == "" {
				subtitle = strings.ReplaceAll(target, "_", " ")
			}

			meta, _ := args["meta"].(string)
			meta = strings.TrimSpace(meta)

			labelParts := []string{title, subtitle}
			if meta != "" {
				labelParts = append(labelParts, meta)
			}
			markdown := "[" + strings.Join(labelParts, " | ") + "](" + url + ")"

			return map[string]string{
				"target":   target,
				"url":      url,
				"markdown": markdown,
			}, nil
		},
	})

	// ========== CHAMADOS DE SUPORTE ==========
	reg.Register(Tool{
		Name:        "get_agent_info",
		Description: "Retorna dados de identificacao do agente: agentId, clientId, siteId, hostname, IP, SO e versao. Use ANTES de criar qualquer chamado (create_ticket) para enriquecer a descricao com informacoes da maquina.",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return app.GetAgentInfoJSON()
		},
	})

	reg.Register(Tool{
		Name:        "list_tickets",
		Description: "Lista os chamados de suporte vinculados a este agente/maquina em formato resumido (id, titulo, descricao curta, categoria, prioridade, workflowStateId, isOpen, createdAt, closedAt), com os ABERTOS primeiro e os contadores total/openCount/returned/truncated. isOpen=true significa chamado ABERTO (ClosedAt nulo); se truncated=true existem chamados fora do recorte. Chame SEMPRE ANTES de create_ticket para verificar se ja existe chamado aberto sobre o MESMO assunto e evitar chamado duplicado; se existir, NAO abra duplicata — informe o usuario e ofereca add_ticket_comment no chamado existente. Use get_ticket_details(ticketId) para o texto completo de um chamado. Use tambem quando o usuario perguntar quais chamados ele tem.",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return app.ListAgentTickets()
		},
	})

	reg.Register(Tool{
		Name:        "get_ticket_details",
		Description: "Retorna os detalhes de um chamado específico do agente autenticado.",
		Params: []ToolParam{
			{Name: "ticketId", Type: "string", Description: "GUID do chamado", Required: true},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			ticketID, _ := args["ticketId"].(string)
			if strings.TrimSpace(ticketID) == "" {
				return nil, fmt.Errorf("ticketId nao pode ser vazio")
			}
			return app.GetAgentTicketDetails(ticketID)
		},
	})

	reg.Register(Tool{
		Name:        "add_ticket_comment",
		Description: "Adiciona um comentário PÚBLICO em um chamado do agente autenticado. Notas internas são exclusivas do portal.",
		Params: []ToolParam{
			{Name: "ticketId", Type: "string", Description: "GUID do chamado", Required: true},
			{Name: "content", Type: "string", Description: "Conteudo do comentario", Required: true},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			ticketID, _ := args["ticketId"].(string)
			if strings.TrimSpace(ticketID) == "" {
				return nil, fmt.Errorf("ticketId nao pode ser vazio")
			}
			content, _ := args["content"].(string)
			if strings.TrimSpace(content) == "" {
				return nil, fmt.Errorf("content nao pode ser vazio")
			}
			// Opção de produto (a): o agente nunca cria nota interna.
			return app.AddAgentTicketComment(ticketID, content)
		},
	})

	reg.Register(Tool{
		Name:        "list_ticket_templates",
		Description: "Lista os modelos (templates) de abertura de chamado disponiveis para esta maquina/cliente, com os campos personalizados de cada um (tipo, obrigatorio, opcoes, mascara). Use ANTES de abrir um chamado: se houver modelos, apresente as opcoes ao usuario (preferencialmente via interface A2UI) OU siga a abertura normal sem template se o usuario preferir.",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return app.ListAgentTicketTemplates()
		},
	})

	reg.Register(Tool{
		Name:        "list_departments",
		Description: "Lista os DEPARTAMENTOS de abertura de chamado disponiveis (globais + do cliente), com id e nome. O departamento define quem atende (auto-atribuicao) e o SLA. Chame ANTES de create_ticket: escolha o departamento que melhor se enquadra no relato do usuario; se nao conseguir decidir, use ask_user mostrando estas opcoes e so abra o chamado depois da resposta.",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return app.ListTicketDepartmentsJSON()
		},
	})

	reg.Register(Tool{
		Name:        "get_department_fields",
		Description: "Lista os CAMPOS PERSONALIZADOS de um departamento (label, tipo, obrigatorio, opcoes, limites). Eles valem para TODO chamado do departamento, com ou sem template; os obrigatorios devem ser enviados em customFields (definitionId->valor) no create_ticket. Chame DEPOIS de escolher o departamento (list_departments) e, se houver campos obrigatorios, pergunte os valores ao usuario antes de abrir o chamado.",
		Params: []ToolParam{
			{Name: "departmentId", Type: "string", Description: "GUID do departamento (obtido em list_departments)", Required: true},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			departmentID, _ := args["departmentId"].(string)
			if strings.TrimSpace(departmentID) == "" {
				return nil, fmt.Errorf("departmentId nao pode ser vazio")
			}
			return app.ListDepartmentFieldsJSON(departmentID)
		},
	})

	reg.Register(Tool{
		Name:        "create_ticket",
		Description: "ABRE um novo chamado de suporte vinculado a esta maquina. PRÉ-REQUISITO ANTIDUPLICIDADE (OBRIGATORIO): antes de abrir, chame list_tickets e confirme que NAO existe chamado aberto (ClosedAt nulo) sobre o mesmo assunto; se existir, NAO abra um chamado novo — avise o usuario e ofereca complementar o existente com add_ticket_comment. Só abra um chamado novo se o usuario confirmar explicitamente que e um problema diferente. Use SEMPRE que o usuario pedir para abrir ticket, chamado, reportar problema ou solicitar suporte, apos a verificacao. O chamado e automaticamente associado ao agente/maquina. Chame get_agent_info antes para enriquecer o titulo e descricao com dados da maquina. Chame get_department_fields para conhecer os campos personalizados obrigatorios do departamento (eles valem mesmo sem template) e envie os valores em customFields (definitionId->valor). Quando um template foi escolhido, envie templateId + answers (respostas do mini questionario, key->valor). NUNCA oriente o usuario a acessar portais web externos.",
		Params: []ToolParam{
			{Name: "title", Type: "string", Description: "Titulo do chamado", Required: true},
			{Name: "description", Type: "string", Description: "Descricao detalhada do problema", Required: true},
			{Name: "priority", Type: "integer", Description: "Prioridade: 1=Baixa, 2=Media, 3=Alta, 4=Critica", Required: false},
			{Name: "category", Type: "string", Description: "Categoria (Hardware, Software, Rede, Acesso, Email, Impressora, VPN, Outro)", Required: false},
			{Name: "templateId", Type: "string", Description: "GUID do template escolhido (opcional; obtenha em list_ticket_templates)", Required: false},
			{Name: "departmentId", Type: "string", Description: "GUID do departamento responsavel (obrigatorio; obtenha em list_departments e escolha o que melhor se enquadra ou pergunte ao usuario)", Required: true},
			{Name: "customFields", Type: "string", Description: "JSON objeto com os valores dos CAMPOS do departamento, mapeando definitionId para valor (opcional)", Required: false},
			{Name: "answers", Type: "string", Description: "JSON objeto com as respostas do mini QUESTIONARIO do template, mapeando a chave da pergunta (key) para o valor (opcional)", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			title, _ := args["title"].(string)
			if strings.TrimSpace(title) == "" {
				return nil, fmt.Errorf("title nao pode ser vazio")
			}
			description, _ := args["description"].(string)
			if strings.TrimSpace(description) == "" {
				return nil, fmt.Errorf("description nao pode ser vazia")
			}
			priority := 2
			if p, ok := args["priority"]; ok {
				switch v := p.(type) {
				case float64:
					priority = int(v)
				case int:
					priority = v
				}
			}
			category, _ := args["category"].(string)
			templateID, _ := args["templateId"].(string)
			departmentID, _ := args["departmentId"].(string)
			if strings.TrimSpace(departmentID) == "" {
				// Departamento é obrigatório na API (responsável + SLA). Falha clara
				// para a IA pedir/decidir o departamento antes de abrir o chamado.
				return nil, fmt.Errorf("departmentId obrigatorio: consulte list_departments e, se nao conseguir decidir, pergunte ao usuario com as opcoes")
			}

			// customFields/answers podem vir como string JSON ou objeto nativo.
			customFieldsJSON := ""
			switch v := args["customFields"].(type) {
			case string:
				customFieldsJSON = v
			case map[string]any:
				if b, err := json.Marshal(v); err == nil {
					customFieldsJSON = string(b)
				}
			}

			answersJSON := ""
			switch v := args["answers"].(type) {
			case string:
				answersJSON = v
			case map[string]any:
				if b, err := json.Marshal(v); err == nil {
					answersJSON = string(b)
				}
			}

			return app.CreateAgentTicket(title, description, priority, category, templateID, departmentID, customFieldsJSON, answersJSON)
		},
	})

	reg.Register(Tool{
		Name:        "close_ticket",
		Description: "ENCERRA um chamado de suporte desta maquina. Use SOMENTE quando o usuario confirmar que o problema foi resolvido (ou pedir explicitamente para fechar). Opcionalmente registre a solucao em comment e a nota do usuario em rating (1..5), sempre perguntando a nota antes de enviar. Chamado encerrado deixa de aceitar comentarios (use reopen_ticket) e, se ja tiver sido avaliado, a nota nao pode ser trocada sem reabrir.",
		Params: []ToolParam{
			{Name: "ticketId", Type: "string", Description: "GUID do chamado", Required: true},
			{Name: "comment", Type: "string", Description: "Resumo da solucao aplicada (opcional)", Required: false},
			{Name: "rating", Type: "integer", Description: "Nota do usuario de 1 a 5 (opcional; pergunte antes de enviar)", Required: false},
			{Name: "workflowStateId", Type: "string", Description: "GUID do estado final desejado (opcional; omita para usar o padrao do workflow)", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			ticketID, _ := args["ticketId"].(string)
			if strings.TrimSpace(ticketID) == "" {
				return nil, fmt.Errorf("ticketId nao pode ser vazio")
			}
			comment, _ := args["comment"].(string)
			workflowStateID, _ := args["workflowStateId"].(string)

			var rating *int
			if raw, ok := args["rating"]; ok && raw != nil {
				value := 0
				switch v := raw.(type) {
				case float64:
					value = int(v)
				case int:
					value = v
				}
				if value != 0 {
					if value < 1 || value > 5 {
						return nil, fmt.Errorf("rating invalido: informe valor entre 1 e 5")
					}
					rating = &value
				}
			}
			return app.CloseAgentTicket(ticketID, rating, comment, workflowStateID)
		},
	})

	reg.Register(Tool{
		Name:        "reopen_ticket",
		Description: "REABRE um chamado encerrado desta maquina: volta ao estado inicial e libera novos comentarios. Use quando o usuario relatar que o problema voltou/persiste apos o encerramento ou quando precisar comentar em um chamado fechado. Reabrir DESCARTA a avaliacao anterior - depois de fechar de novo, o usuario podera avaliar novamente.",
		Params: []ToolParam{
			{Name: "ticketId", Type: "string", Description: "GUID do chamado", Required: true},
			{Name: "reason", Type: "string", Description: "Motivo da reabertura (opcional)", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			ticketID, _ := args["ticketId"].(string)
			if strings.TrimSpace(ticketID) == "" {
				return nil, fmt.Errorf("ticketId nao pode ser vazio")
			}
			reason, _ := args["reason"].(string)
			return app.ReopenAgentTicket(ticketID, reason)
		},
	})

	reg.Register(Tool{
		Name:        "rate_ticket",
		Description: "REGISTRA a avaliacao (CSAT 1..5) e o feedback do usuario para um chamado ENCERRADO desta maquina. Pergunte a nota ao usuario antes de enviar. Chamado aberto nao pode ser avaliado; se ja estiver avaliado, a nota anterior se mantem - para avaliar de novo, reabra o chamado com reopen_ticket e feche-o outra vez.",
		Params: []ToolParam{
			{Name: "ticketId", Type: "string", Description: "GUID do chamado", Required: true},
			{Name: "rating", Type: "integer", Description: "Nota de 1 a 5", Required: true},
			{Name: "feedback", Type: "string", Description: "Comentario do usuario sobre o atendimento (opcional)", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			ticketID, _ := args["ticketId"].(string)
			if strings.TrimSpace(ticketID) == "" {
				return nil, fmt.Errorf("ticketId nao pode ser vazio")
			}
			rating := 0
			if raw, ok := args["rating"]; ok {
				switch v := raw.(type) {
				case float64:
					rating = int(v)
				case int:
					rating = v
				}
			}
			if rating < 1 || rating > 5 {
				return nil, fmt.Errorf("rating obrigatorio: informe valor entre 1 e 5")
			}
			feedback, _ := args["feedback"].(string)
			return app.RateAgentTicket(ticketID, rating, feedback)
		},
	})

	// ========== EVENT LOG ==========
	reg.Register(Tool{
		Name:        "query_event_log",
		Description: "Consulta o Windows Event Log com filtros. logName: System, Application ou Setup. level: Critical, Error, Warning ou Information (vazio = todos). source: nome do provider/fonte (opcional). maxEvents: maximo de eventos retornados (padrao 50, max 100). lastHours: janela de tempo em horas (padrao 24, max 168).",
		Params: []ToolParam{
			{Name: "logName", Type: "string", Description: "Nome do log: System, Application ou Setup", Required: true},
			{Name: "level", Type: "string", Description: "Nivel: Critical, Error, Warning, Information (vazio = todos)", Required: false},
			{Name: "source", Type: "string", Description: "Fonte/Provider do evento (ex: Service Control Manager)", Required: false},
			{Name: "maxEvents", Type: "integer", Description: "Maximo de eventos retornados (padrao 50, max 100)", Required: false},
			{Name: "lastHours", Type: "integer", Description: "Janela de tempo em horas (padrao 24, max 168)", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			logName, _ := args["logName"].(string)
			level, _ := args["level"].(string)
			source, _ := args["source"].(string)
			maxEvents := 0
			if v, ok := args["maxEvents"]; ok {
				switch n := v.(type) {
				case float64:
					maxEvents = int(n)
				case int:
					maxEvents = n
				}
			}
			lastHours := 0
			if v, ok := args["lastHours"]; ok {
				switch n := v.(type) {
				case float64:
					lastHours = int(n)
				case int:
					lastHours = n
				}
			}
			return QueryEventLog(ctx, logName, level, source, maxEvents, lastHours)
		},
	})

	reg.Register(Tool{
		Name:        "get_recent_errors",
		Description: "Retorna eventos Critical e Error dos logs System e Application nas ultimas N horas (padrao 24h, max 168h). Util para diagnostico rapido de problemas sem abrir o Visualizador de Eventos.",
		Params: []ToolParam{
			{Name: "lastHours", Type: "integer", Description: "Janela de tempo em horas (padrao 24, max 168)", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			lastHours := 0
			if v, ok := args["lastHours"]; ok {
				switch n := v.(type) {
				case float64:
					lastHours = int(n)
				case int:
					lastHours = n
				}
			}
			return GetRecentErrors(ctx, lastHours)
		},
	})

	reg.Register(Tool{
		Name:        "get_recent_crashes",
		Description: "Retorna BSODs, falhas de kernel (ID 41), desligamentos inesperados (ID 6008) e crashes de aplicativo (IDs 1000/1001) nas ultimas N horas (padrao 168h = 7 dias).",
		Params: []ToolParam{
			{Name: "lastHours", Type: "integer", Description: "Janela de tempo em horas (padrao 168 = 7 dias, max 168)", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			lastHours := 0
			if v, ok := args["lastHours"]; ok {
				switch n := v.(type) {
				case float64:
					lastHours = int(n)
				case int:
					lastHours = n
				}
			}
			return GetRecentCrashes(ctx, lastHours)
		},
	})

	// ========== DESEMPENHO ==========
	reg.Register(Tool{
		Name:        "get_performance_snapshot",
		Description: "Retorna um snapshot instantaneo de desempenho: uso de CPU (%), memoria (total/usada/livre em GB e %) e uso de disco por volume logico.",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return GetPerformanceSnapshot(ctx)
		},
	})

	reg.Register(Tool{
		Name:        "get_top_processes",
		Description: "Retorna os N processos com maior consumo de CPU (segundos acumulados) ou memoria (MB). CommandLine e omitido por seguranca. top: 1-50 (padrao 10). orderBy: cpu ou memory (padrao cpu).",
		Params: []ToolParam{
			{Name: "top", Type: "integer", Description: "Numero de processos a retornar (1-50, padrao 10)", Required: false},
			{Name: "orderBy", Type: "string", Description: "Criterio de ordenacao: cpu ou memory (padrao cpu)", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			top := 0
			if v, ok := args["top"]; ok {
				switch n := v.(type) {
				case float64:
					top = int(n)
				case int:
					top = n
				}
			}
			orderBy, _ := args["orderBy"].(string)
			return GetTopProcesses(ctx, top, orderBy)
		},
	})

	reg.Register(Tool{
		Name:        "get_disk_health",
		Description: "Retorna o status de saude dos discos fisicos via WMI (Win32_DiskDrive). Inclui modelo, fabricante, serial, interface, tamanho e status (OK / Pred Fail / Unknown). Util para identificar discos com falha iminente.",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return GetDiskHealth(ctx)
		},
	})

	// ========== PERGUNTA INTERATIVA ==========
	reg.Register(Tool{
		Name: "ask_user",
		Description: "Faz uma pergunta ao usuario com opcoes clicaveis e aguarda a resposta. " +
			"Use SEMPRE que precisar de confirmacao, escolha entre alternativas ou esclarecimento do usuario. " +
			"A ferramenta BLOQUEIA ate o usuario responder (sem limite de tempo) — use apenas quando realmente precisar de input. " +
			"Prefira usar texto com botoes (- opcao) para perguntas simples que nao bloqueiam o fluxo.",
		Params: []ToolParam{
			{Name: "question", Type: "string", Description: "A pergunta a ser exibida ao usuario (ex: 'Qual programa voce quer instalar?')", Required: true},
			{Name: "options", Type: "string", Description: "JSON array de opcoes clicaveis. Ex: '[\"Google Chrome\",\"Firefox\",\"Outro\"]'. Maximo 6. Use [] para so texto livre.", Required: false},
			{Name: "allowText", Type: "string", Description: "Se 'true', mostra campo de texto livre para o usuario digitar uma resposta personalizada", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			question, err := requiredStringArg(args, "question")
			if err != nil {
				return nil, err
			}
			// options pode vir como string JSON ou como array nativo do JSON unmarshal
			optionsJSON := ""
			switch v := args["options"].(type) {
			case string:
				optionsJSON = v
			case []any:
				b, _ := json.Marshal(v)
				optionsJSON = string(b)
			}
			// allowText pode vir como string "true"/"false", bool nativo ou número
			allowText := "false"
			switch v := args["allowText"].(type) {
			case string:
				allowText = v
			case bool:
				if v {
					allowText = "true"
				}
			case float64:
				if v != 0 {
					allowText = "true"
				}
			}

			// Com ctx: o cancelamento do stream (botão Parar) interrompe a
			// espera pela resposta em vez de deixar a goroutine pendurada. A
			// pergunta em si não tem limite de tempo — o chat prossegue
			// quando o usuário responder (B5).
			answer, err := app.AskUserChatWithContext(ctx, question, optionsJSON, allowText)
			if err != nil {
				return nil, err
			}
			return map[string]string{"answer": answer}, nil
		},
	})

	// ========== CAPTURA DE TELA (visao do LLM) ==========
	reg.Register(Tool{
		Name: "list_open_windows",
		Description: "Lista as janelas visiveis abertas no desktop (handle, titulo, processo, PID, posicao/tamanho, foco). " +
			"Use ANTES de capture_screenshot(mode=window) para escolher o alvo do diagnostico. " +
			"Janelas com blocked=true estao na blocklist de privacidade e NAO podem ser capturadas. " +
			"Nao captura nada e nao exige autorizacao.",
		Params: []ToolParam{
			{Name: "includeUntitled", Type: "boolean", Description: "Inclui janelas sem titulo (games/UWP); padrao false", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			includeUntitled := false
			switch v := args["includeUntitled"].(type) {
			case bool:
				includeUntitled = v
			case string:
				switch strings.ToLower(strings.TrimSpace(v)) {
				case "true", "1", "yes", "sim", "on":
					includeUntitled = true
				}
			case float64:
				includeUntitled = v != 0
			}
			return app.ListOpenWindowsJSON(includeUntitled)
		},
	})

	reg.Register(Tool{
		Name: "capture_screenshot",
		Description: "Captura a tela deste computador para diagnostico visual e devolve a imagem ao modelo (visao). " +
			"Modos: full (todos os monitores), window (exige windowHandle de list_open_windows), focused (janela em foco), " +
			"monitor (exige monitor), region (exige x, y, width, height) e interactive (o usuario seleciona a area/janela na tela congelada). " +
			"A PRIMEIRA captura pedida pela IA exige autorizacao explicita do usuario: o agente abre um pedido de permissao " +
			"no chat; depois de autorizada (nesta conversa ou sempre), a IA pode capturar de forma automatica. " +
			"Nunca use para espionar: informe sempre o motivo em reason e prefira a janela especifica em vez da tela inteira.",
		Params: []ToolParam{
			{Name: "mode", Type: "string", Description: "full | window | focused | monitor | region | interactive", Required: true},
			{Name: "windowHandle", Type: "integer", Description: "Handle da janela (list_open_windows) — modo window", Required: false},
			{Name: "monitor", Type: "integer", Description: "Indice do monitor (0 = primario) — modo monitor", Required: false},
			{Name: "x", Type: "integer", Description: "Coordenada X fisica da regiao — modo region", Required: false},
			{Name: "y", Type: "integer", Description: "Coordenada Y fisica da regiao — modo region", Required: false},
			{Name: "width", Type: "integer", Description: "Largura da regiao — modo region", Required: false},
			{Name: "height", Type: "integer", Description: "Altura da regiao — modo region", Required: false},
			{Name: "reason", Type: "string", Description: "Motivo da captura exibido ao usuario no pedido de autorizacao", Required: false},
			{Name: "quality", Type: "integer", Description: "Qualidade JPEG 1-100 (padrao 80; PNG e usado quando menor)", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return app.CaptureScreenshotForTool(ctx, args)
		},
	})

	reg.Register(Tool{
		Name: "screenshot_permission",
		Description: "Consulta o estado da autorizacao de captura de tela (undecided, session, always ou denied) e as ultimas capturas. " +
			"Use quando capture_screenshot falhar por falta de autorizacao para orientar o usuario.",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return app.ScreenshotConsentStatusJSON()
		},
	})
}

func requiredStringArg(args map[string]any, name string) (string, error) {
	value, _ := args[name].(string)
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s nao pode ser vazio", name)
	}
	return value, nil
}

func optionalStringArg(args map[string]any, name string) string {
	value, _ := args[name].(string)
	return strings.TrimSpace(value)
}

func requiredIntArg(args map[string]any, name string) (int, error) {
	value, ok := args[name]
	if !ok {
		return 0, fmt.Errorf("%s nao pode ser vazio", name)
	}

	switch typed := value.(type) {
	case int:
		if typed <= 0 {
			return 0, fmt.Errorf("%s deve ser maior que zero", name)
		}
		return typed, nil
	case float64:
		parsed := int(typed)
		if float64(parsed) != typed || parsed <= 0 {
			return 0, fmt.Errorf("%s deve ser um inteiro maior que zero", name)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("%s deve ser um inteiro", name)
	}
}

// ── M31: gate de confirmação para tools destrutivas ────────────────────────

// requireConfirm valida o parâmetro confirm=true nas tools destrutivas do MCP.
// Sem isso, ações como uninstall/upgrade_all/restart_spooler dependiam do LLM
// usar ask_user voluntariamente — agora o servidor exige a confirmação
// explícita no payload da chamada (o LLM precisa ter obtido o ok do usuário).
func requireConfirm(args map[string]any) error {
	if v, ok := args["confirm"].(bool); ok && v {
		return nil
	}
	return fmt.Errorf("acao destrutiva: exige confirmacao explicita do usuario — envie \"confirm\": true no payload da tool apos o usuario aprovar")
}
