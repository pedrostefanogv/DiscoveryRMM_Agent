package mcp

import (
	"context"
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
	// Tickets
	GetAgentInfoJSON() (json.RawMessage, error)
	ListAgentTickets() (json.RawMessage, error)
	GetAgentTicketDetails(ticketID string) (json.RawMessage, error)
	AddAgentTicketComment(ticketID, content string, isInternal bool) (json.RawMessage, error)
	CreateAgentTicket(title, description string, priority int, category string) (json.RawMessage, error)
}

// RegisterDiscoveryTools adds all Discovery app tools to the registry.
func RegisterDiscoveryTools(reg *Registry, app AppBridge) {
	// ========== INVENTARIO ==========
	reg.Register(Tool{
		Name:        "get_inventory",
		Description: "Retorna o inventario completo do computador: hardware, SO, discos, rede, usuarios logados, bateria, CPU, GPU, memoria, BitLocker, software instalado, startup items.",
		Handler: func(args map[string]any) (any, error) {
			return app.GetInventoryJSON()
		},
	})

	reg.Register(Tool{
		Name:        "export_inventory_markdown",
		Description: "Exporta o relatorio de inventario em formato Markdown e retorna o caminho do arquivo gerado.",
		Handler: func(args map[string]any) (any, error) {
			path, err := app.ExportMarkdown()
			return map[string]string{"path": path}, err
		},
	})

	reg.Register(Tool{
		Name:        "export_inventory_pdf",
		Description: "Exporta o relatorio de inventario em formato PDF e retorna o caminho do arquivo gerado.",
		Handler: func(args map[string]any) (any, error) {
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
		Handler: func(args map[string]any) (any, error) {
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
		Handler: func(args map[string]any) (any, error) {
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
		Handler: func(args map[string]any) (any, error) {
			out, err := app.ListInstalled()
			return map[string]string{"output": out}, err
		},
	})

	reg.Register(Tool{
		Name:        "uninstall_package",
		Description: "Desinstala um pacote via winget pelo seu ID.",
		Params: []ToolParam{
			{Name: "id", Type: "string", Description: "ID do pacote winget", Required: true},
		},
		Handler: func(args map[string]any) (any, error) {
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
		Handler: func(args map[string]any) (any, error) {
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
		Handler: func(args map[string]any) (any, error) {
			return app.GetPendingUpdatesJSON()
		},
	})

	reg.Register(Tool{
		Name:        "get_package_actions",
		Description: "Retorna o mapa de acao contextual por pacote (install, uninstall, upgrade).",
		Handler: func(args map[string]any) (any, error) {
			return app.GetPackageActionsJSON()
		},
	})

	reg.Register(Tool{
		Name:        "upgrade_all_packages",
		Description: "Atualiza todos os pacotes que possuem atualizacao disponivel via winget.",
		Handler: func(args map[string]any) (any, error) {
			out, err := app.UpgradeAllPackages()
			return map[string]string{"output": out}, err
		},
	})

	// ========== SISTEMA E DIAGNOSTICOS ==========
	reg.Register(Tool{
		Name:        "get_osquery_status",
		Description: "Verifica se o osquery esta instalado no computador e retorna o caminho do binario.",
		Handler: func(args map[string]any) (any, error) {
			return app.GetOsqueryStatusJSON()
		},
	})

	// ========== IMPRESSORAS ==========
	reg.Register(Tool{
		Name:        "list_printers",
		Description: "Lista as impressoras instaladas no Windows com driver, porta e status.",
		Handler: func(args map[string]any) (any, error) {
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
		Handler: func(args map[string]any) (any, error) {
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
		Handler: func(args map[string]any) (any, error) {
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
		Handler: func(args map[string]any) (any, error) {
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
		Handler: func(args map[string]any) (any, error) {
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
		Handler: func(args map[string]any) (any, error) {
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
		Handler: func(args map[string]any) (any, error) {
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
		Handler: func(args map[string]any) (any, error) {
			return app.GetSpoolerStatusJSON()
		},
	})

	reg.Register(Tool{
		Name:        "restart_spooler",
		Description: "Reinicia o servico Spooler e retorna o status apos o restart.",
		Handler: func(args map[string]any) (any, error) {
			return app.RestartSpoolerJSON()
		},
	})

	reg.Register(Tool{
		Name:        "clear_queue",
		Description: "Limpa todos os jobs pendentes da fila de uma impressora.",
		Params: []ToolParam{
			{Name: "printerName", Type: "string", Description: "Nome da impressora", Required: true},
		},
		Handler: func(args map[string]any) (any, error) {
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
		Handler: func(args map[string]any) (any, error) {
			return app.ListPrinterDriversJSON()
		},
	})

	reg.Register(Tool{
		Name:        "get_logs",
		Description: "Retorna os logs recentes de operacoes do winget (instalacao, atualizacao, etc).",
		Handler: func(args map[string]any) (any, error) {
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
		Handler: func(args map[string]any) (any, error) {
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
			return PingHost(context.Background(), host, count, timeout)
		},
	})

	reg.Register(Tool{
		Name:        "flush_dns",
		Description: "Limpa o cache DNS do sistema (ipconfig /flushdns no Windows).",
		Handler: func(args map[string]any) (any, error) {
			return FlushDNS(context.Background())
		},
	})

	// ========== NAVEGACAO INTERNA ==========
	reg.Register(Tool{
		Name:        "get_internal_navigation_routes",
		Description: "Lista as rotas internas disponiveis no app para construir links discovery:// clicaveis no chat.",
		Handler: func(args map[string]any) (any, error) {
			return []map[string]string{
				{"target": "support_tickets", "url": "discovery://support/tickets", "description": "Abre a tela de chamados"},
				{"target": "support_ticket", "url": "discovery://support/ticket/{ticketId}", "description": "Abre chamado especifico"},
				{"target": "store", "url": "discovery://store", "description": "Abre a aba Loja"},
				{"target": "updates", "url": "discovery://updates", "description": "Abre a aba Atualizacoes"},
				{"target": "inventory", "url": "discovery://inventory", "description": "Abre a aba Inventario"},
				{"target": "logs", "url": "discovery://logs", "description": "Abre a aba Logs"},
				{"target": "chat", "url": "discovery://chat", "description": "Abre a aba Chat IA"},
				{"target": "knowledge", "url": "discovery://knowledge", "description": "Abre a Base de Conhecimento"},
				{"target": "debug", "url": "discovery://debug", "description": "Abre a aba Debug"},
			}, nil
		},
	})

	reg.Register(Tool{
		Name:        "build_internal_navigation_link",
		Description: "Monta um link interno discovery:// e um markdown de card clicavel para navegação interna no app.",
		Params: []ToolParam{
			{Name: "target", Type: "string", Description: "Destino: support_tickets, support_ticket, store, updates, inventory, logs, chat, knowledge, debug", Required: true},
			{Name: "ticketId", Type: "string", Description: "GUID do chamado (obrigatorio apenas para target=support_ticket)", Required: false},
			{Name: "title", Type: "string", Description: "Titulo do card/botao", Required: false},
			{Name: "subtitle", Type: "string", Description: "Subtitulo do card", Required: false},
			{Name: "meta", Type: "string", Description: "Meta adicional do card", Required: false},
		},
		Handler: func(args map[string]any) (any, error) {
			target, _ := args["target"].(string)
			target = strings.TrimSpace(strings.ToLower(target))
			if target == "" {
				return nil, fmt.Errorf("target nao pode ser vazio")
			}

			ticketID, _ := args["ticketId"].(string)
			ticketID = strings.TrimSpace(ticketID)

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
		Description: "Retorna as informacoes do agente atual: agentId, clientId, siteId, hostname.",
		Handler: func(args map[string]any) (any, error) {
			return app.GetAgentInfoJSON()
		},
	})

	reg.Register(Tool{
		Name:        "list_tickets",
		Description: "Lista os chamados de suporte abertos para este agente/maquina.",
		Handler: func(args map[string]any) (any, error) {
			return app.ListAgentTickets()
		},
	})

	reg.Register(Tool{
		Name:        "get_ticket_details",
		Description: "Retorna os detalhes de um chamado específico do agente autenticado.",
		Params: []ToolParam{
			{Name: "ticketId", Type: "string", Description: "GUID do chamado", Required: true},
		},
		Handler: func(args map[string]any) (any, error) {
			ticketID, _ := args["ticketId"].(string)
			if strings.TrimSpace(ticketID) == "" {
				return nil, fmt.Errorf("ticketId nao pode ser vazio")
			}
			return app.GetAgentTicketDetails(ticketID)
		},
	})

	reg.Register(Tool{
		Name:        "add_ticket_comment",
		Description: "Adiciona um comentário em um chamado do agente autenticado.",
		Params: []ToolParam{
			{Name: "ticketId", Type: "string", Description: "GUID do chamado", Required: true},
			{Name: "content", Type: "string", Description: "Conteudo do comentario", Required: true},
			{Name: "isInternal", Type: "boolean", Description: "Se true, cria comentario interno", Required: false},
		},
		Handler: func(args map[string]any) (any, error) {
			ticketID, _ := args["ticketId"].(string)
			if strings.TrimSpace(ticketID) == "" {
				return nil, fmt.Errorf("ticketId nao pode ser vazio")
			}
			content, _ := args["content"].(string)
			if strings.TrimSpace(content) == "" {
				return nil, fmt.Errorf("content nao pode ser vazio")
			}
			isInternal := false
			if v, ok := args["isInternal"]; ok {
				if parsed, ok := v.(bool); ok {
					isInternal = parsed
				}
			}
			return app.AddAgentTicketComment(ticketID, content, isInternal)
		},
	})

	reg.Register(Tool{
		Name:        "create_ticket",
		Description: "Abre um novo chamado de suporte vinculado automaticamente a esta maquina/agente.",
		Params: []ToolParam{
			{Name: "title", Type: "string", Description: "Titulo do chamado", Required: true},
			{Name: "description", Type: "string", Description: "Descricao detalhada do problema", Required: true},
			{Name: "priority", Type: "integer", Description: "Prioridade: 1=Baixa, 2=Media, 3=Alta, 4=Critica", Required: false},
			{Name: "category", Type: "string", Description: "Categoria (Hardware, Software, Rede, Acesso, Email, Impressora, VPN, Outro)", Required: false},
		},
		Handler: func(args map[string]any) (any, error) {
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
			return app.CreateAgentTicket(title, description, priority, category)
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
