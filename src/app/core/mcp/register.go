package mcp

import (
	"context"
	"discovery/app/core/automation"
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
	RunOsqueryQueryJSON(sql string, limit int) (json.RawMessage, error)
	SendAgentNotification(title, message, level string) (json.RawMessage, error)
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

	// Anexos de imagem: turno do chat e capturas de tela.
	// GetLastTurnImage devolve a data URL da N-ésima imagem (1-based) enviada
	// pelo usuário no último turno do chat.
	GetLastTurnImage(index int) (string, error)
	AttachImagesToTicket(ticketID string, images []TicketImageUpload) ([]string, error)
	AttachImagesToComment(ticketID, commentID string, images []TicketImageUpload) ([]string, error)
	ListAgentTicketAttachments(ticketID string) (json.RawMessage, error)
	// DownloadAgentTicketAttachment baixa um anexo (do chamado ou, quando
	// commentID != "", do comentário) para a IA visualizar como imagem.
	DownloadAgentTicketAttachment(ticketID, attachmentID, commentID string) (json.RawMessage, error)

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

	// ReadFileWithConsent le um arquivo de TEXTO do computador do usuario com
	// autorizacao obrigatoria NESTA leitura (o usuario aprova ou nega cada
	// pedido; nao existe "permitir sempre").
	ReadFileWithConsent(ctx context.Context, path string, maxBytes int, reason string) (json.RawMessage, error)

	// RunPowerAction executa restart/shutdown/lock pedidos pela IA. Reinicio e
	// desligamento exibem aviso do PSADT com contador e botao CANCELAR; se o
	// usuario cancelar, nada e executado.
	RunPowerAction(ctx context.Context, action string, delaySeconds int, message string) (json.RawMessage, error)

	// OpenFolder/OpenApp abrem uma pasta no Explorer ou um aplicativo JA
	// INSTALADO, pedidos pela IA. A autorização por ação vem de
	// mcp.ToolConsentFor (fail-closed) e a resolução/validação do alvo fica no
	// App: a tool nunca recebe linha de comando nem caminho arbitrário.
	OpenFolder(folder, reason string) (json.RawMessage, error)
	OpenApp(name, reason string) (json.RawMessage, error)
	// ListInstalledApps lista nomes de apps instalados (leitura pura, sem
	// autorização) para o LLM não adivinhar o nome antes de open_app.
	ListInstalledApps(query string, limit int) (json.RawMessage, error)

	// SensorDebugSnapshotForMCP entrega o retrato do motor de sensores para a
	// tool get_sensors (LEITURA). É o MESMO snapshot do painel de debug
	// (App.SensorDebugSnapshotCore, com o bridge companion antes do motor
	// local) — a tool NÃO reimplementa coleta: ela só recorta/expoe o que o
	// núcleo já monta.
	SensorDebugSnapshotForMCP() automation.SensorDebugSnapshot
}

// RegisterDiscoveryTools adds all Discovery app tools to the registry.
func RegisterDiscoveryTools(reg *Registry, app AppBridge) {
	// Abrir pasta/app: ações visíveis no computador, com consentimento por ação.
	registerOpenExternalTools(reg, app)

	// ========== INVENTARIO ==========
	reg.Register(Tool{
		Name:        "get_inventory",
		Description: "Retorna o inventario COMPLETO e pesado do computador (hardware, SO, discos, rede, usuarios, bateria, CPU, GPU, memoria, BitLocker, software instalado, startup items). Use APENAS quando precisar do inventario completo; para checagens rapidas de SO/CPU/RAM/disco use system_info ou get_performance_snapshot.",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return app.GetInventoryJSON()
		},
	})

	reg.Register(Tool{
		Name: "export_inventory_markdown",
		Description: "Exporta o relatorio de inventario em formato Markdown e retorna o caminho do arquivo gerado. " +
			"A GRAVACAO do arquivo exige AUTORIZACAO do usuario (pergunta exibida no chat ANTES da tool rodar); " +
			"se o usuario negar, o resultado vem com approved=false e a chamada NAO deve ser repetida.",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			if err := requireConfirm(args); err != nil {
				return nil, err
			}
			path, err := app.ExportMarkdown()
			return map[string]string{"path": path}, err
		},
	})

	reg.Register(Tool{
		Name: "export_inventory_pdf",
		Description: "Exporta o relatorio de inventario em formato PDF e retorna o caminho do arquivo gerado. " +
			"A GRAVACAO do arquivo exige AUTORIZACAO do usuario (pergunta exibida no chat ANTES da tool rodar); " +
			"se o usuario negar, o resultado vem com approved=false e a chamada NAO deve ser repetida.",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			if err := requireConfirm(args); err != nil {
				return nil, err
			}
			path, err := app.ExportPDF()
			return map[string]string{"path": path}, err
		},
	})

	// ========== SOFTWARE & MEMORIA LOCAL (CONSOLIDADOS) ==========
	registerSoftwareTool(reg, app)
	registerLocalMemoryTool(reg, app)

	reg.Register(Tool{
		Name:        "get_logs",
		Description: "Retorna os logs recentes de operacoes do winget (instalacao, atualizacao, etc).",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return map[string]string{"logs": app.GetLogsText()}, nil
		},
	})

	// ========== LEITURA DE ARQUIVO (com autorizacao do usuario) ==========
	reg.Register(Tool{
		Name: "read_file",
		Description: "Le o conteudo de um arquivo de TEXTO no computador do usuario (somente leitura; nunca escreve nem apaga). " +
			"TODA leitura exige AUTORIZACAO EXPLICITA do usuario NESTA leitura: o agente mostra caminho, tamanho e motivo e " +
			"so le o arquivo se o usuario aprovar (nao existe 'permitir sempre'). Sem aprovacao, devolve approved=false. " +
			"Arquivos binarios sao recusados. Le no maximo 10 KB por padrao (ajustavel ate 64 KB). " +
			"Use SEMPRE que o usuario pedir para ler/ver o conteudo de um arquivo e explique o motivo em reason.",
		Params: []ToolParam{
			{Name: "path", Type: "string", Description: "Caminho ABSOLUTO do arquivo de texto (ex: C:\\Users\\usuario\\Documents\\config.ini)", Required: true},
			{Name: "maxBytes", Type: "integer", Description: "Limite de bytes a ler (padrao 10240, max 65536)", Required: false},
			{Name: "reason", Type: "string", Description: "Motivo da leitura, exibido ao usuario no pedido de autorizacao (OBRIGATORIO: o usuario decide com base nele)", Required: true},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			path, err := requiredStringArg(args, "path")
			if err != nil {
				return nil, err
			}
			maxBytes := optionalIntArg(args, "maxBytes")
			reason, err := requiredStringArg(args, "reason")
			if err != nil {
				return nil, err
			}
			if ctx == nil {
				ctx = context.Background()
			}
			return app.ReadFileWithConsent(ctx, path, maxBytes, reason)
		},
	})

	// ========== CHAMADOS DE SUPORTE (leitura movida ao servidor; rotas discovery:// no prompt) ==========
	reg.Register(Tool{
		Name:        "get_agent_info",
		Description: "Retorna dados de identificacao do agente: agentId, clientId, siteId, hostname, IP, SO e versao. Use ANTES de criar qualquer chamado (create_ticket) para enriquecer a descricao com informacoes da maquina.",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return app.GetAgentInfoJSON()
		},
	})

	// ========== ANEXOS DE IMAGEM EM CHAMADOS ==========
	// As refs apontam para imagens que o usuário já enviou no chat (turn:N) ou
	// para capturas de tela da própria IA (shot:last/shot:<hash>). O envio ao
	// servidor exige consentimento por ação (ver ToolConsentFor).
	reg.Register(Tool{
		Name: "attach_images_to_ticket",
		Description: "Anexa imagens ao REPOSITÓRIO de um chamado do agente autenticado. " +
			"Use SOMENTE quando o usuario pedir ou confirmar explicitamente que quer anexar a imagem ao chamado; " +
			"a acao ENVIA a imagem do usuario ao servidor e exige autorizacao dele no chat. " +
			"imageRefs aceita uma string separada por virgula ou array JSON. Refs: turn:N = N-esima imagem que o usuario " +
			"mandou no chat no ultimo turno (1-based); shot:last = ultima captura de tela feita por screen(action=\"capture\"); " +
			"shot:<sha256> = captura especifica (o screen(action=\"capture\") devolve image_ref). " +
			"Formatos: png, jpeg, webp, gif; maximo 10 MB binarios por imagem.",
		Params: []ToolParam{
			{Name: "ticketId", Type: "string", Description: "GUID do chamado", Required: true},
			{Name: "imageRefs", Type: "string", Description: "Refs das imagens: turn:1; turn:1,turn:2; shot:last; ou array JSON com essas refs", Required: true},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			if err := requireConfirm(args); err != nil {
				return nil, err
			}
			ticketID, err := requiredStringArg(args, "ticketId")
			if err != nil {
				return nil, err
			}
			refs := stringOrArrayArg(args, "imageRefs")
			if refs == "" {
				return nil, fmt.Errorf("parametro obrigatorio 'imageRefs' nao pode ser vazio")
			}
			images, err := resolveImageRefs(refs, app)
			if err != nil {
				return nil, err
			}
			ids, err := app.AttachImagesToTicket(ticketID, images)
			if err != nil {
				// Upload parcial: devolve os IDs já criados para a IA não
				// reenviar/anexar duplicado ao repetir a operação.
				return map[string]any{
					"ok":          false,
					"ticketId":    ticketID,
					"attachments": attachmentMetadata(images, ids),
					"error":       fmt.Sprintf("falha ao anexar imagens ao chamado %s: %v", ticketID, err),
				}, nil
			}
			return map[string]any{
				"ok":          true,
				"ticketId":    ticketID,
				"attachments": attachmentMetadata(images, ids),
			}, nil
		},
	})

	reg.Register(Tool{
		Name: "attach_images_to_ticket_comment",
		Description: "Anexa imagens a um COMENTARIO de chamado do agente autenticado (a conversa do chamado). " +
			"Use SOMENTE quando o usuario pedir ou confirmar explicitamente que quer anexar a imagem ao comentario; " +
			"a acao ENVIA a imagem do usuario ao servidor e exige autorizacao dele no chat. " +
			"imageRefs aceita string separada por virgula ou array JSON. Refs: turn:N = N-esima imagem que o usuario " +
			"mandou no chat no ultimo turno (1-based); shot:last = ultima captura de tela; shot:<sha256> = captura especifica. " +
			"O comentario precisa existir antes (add_ticket_comment devolve commentId).",
		Params: []ToolParam{
			{Name: "ticketId", Type: "string", Description: "GUID do chamado", Required: true},
			{Name: "commentId", Type: "string", Description: "GUID do comentario que recebera as imagens", Required: true},
			{Name: "imageRefs", Type: "string", Description: "Refs das imagens (turn:N, shot:last, shot:<sha256>)", Required: true},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			if err := requireConfirm(args); err != nil {
				return nil, err
			}
			ticketID, err := requiredStringArg(args, "ticketId")
			if err != nil {
				return nil, err
			}
			commentID, err := requiredStringArg(args, "commentId")
			if err != nil {
				return nil, err
			}
			refs := stringOrArrayArg(args, "imageRefs")
			if refs == "" {
				return nil, fmt.Errorf("parametro obrigatorio 'imageRefs' nao pode ser vazio")
			}
			images, err := resolveImageRefs(refs, app)
			if err != nil {
				return nil, err
			}
			ids, err := app.AttachImagesToComment(ticketID, commentID, images)
			if err != nil {
				return map[string]any{
					"ok":          false,
					"ticketId":    ticketID,
					"commentId":   commentID,
					"attachments": attachmentMetadata(images, ids),
					"error":       fmt.Sprintf("falha ao anexar imagens ao comentario %s do chamado %s: %v", commentID, ticketID, err),
				}, nil
			}
			return map[string]any{
				"ok":          true,
				"ticketId":    ticketID,
				"commentId":   commentID,
				"attachments": attachmentMetadata(images, ids),
			}, nil
		},
	})

	reg.Register(Tool{
		Name:        "list_ticket_attachments",
		Description: "Lista os anexos de um chamado do agente autenticado (repositorio + conversa) apenas com METADADOS: id, fileName, contentType, sizeBytes, origin, commentId e datas. Nao baixa os bytes. Use antes de anexar/imprimir para conferir o que ja existe.",
		Params: []ToolParam{
			{Name: "ticketId", Type: "string", Description: "GUID do chamado", Required: true},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			ticketID, err := requiredStringArg(args, "ticketId")
			if err != nil {
				return nil, err
			}
			return app.ListAgentTicketAttachments(ticketID)
		},
	})

	reg.Register(Tool{
		Name: "view_ticket_attachment",
		Description: "Visualiza uma IMAGEM anexada a um chamado (ou a um comentario) para analisar o conteudo com visao. " +
			"Use quando o usuario pedir para ver/analisar um print/anexo do chamado; o anexo e baixado do servidor e a imagem " +
			"segue para o modelo. Aceita apenas png, jpeg, webp ou gif de ate 10 MiB; outros tipos/arquivos devolvem erro claro. " +
			"O download ENVIA o pedido ao servidor e exige autorizacao do usuario no chat. Para listar os IDs use list_ticket_attachments; " +
			"informe commentId quando o anexo for da conversa (origin=comment).",
		Params: []ToolParam{
			{Name: "ticketId", Type: "string", Description: "GUID do chamado", Required: true},
			{Name: "attachmentId", Type: "string", Description: "GUID do anexo (obtido em list_ticket_attachments)", Required: true},
			{Name: "commentId", Type: "string", Description: "GUID do comentario quando o anexo for da conversa (opcional; vazio = anexo do repositorio do chamado)", Required: false},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			if err := requireConfirm(args); err != nil {
				return nil, err
			}
			ticketID, err := requiredStringArg(args, "ticketId")
			if err != nil {
				return nil, err
			}
			attachmentID, err := requiredStringArg(args, "attachmentId")
			if err != nil {
				return nil, err
			}
			commentID := optionalStringArg(args, "commentId")
			return app.DownloadAgentTicketAttachment(ticketID, attachmentID, commentID)
		},
	})

	reg.Register(Tool{
		Name: "add_ticket_comment",
		Description: "Adiciona um comentário PÚBLICO em um chamado do agente autenticado. Notas internas são exclusivas do portal. " +
			"Opcionalmente, attachImageRefs anexa imagens AO COMENTARIO: aceita string separada por virgula ou array JSON com " +
			"turn:N (N-esima imagem que o usuario mandou no chat no ultimo turno, 1-based), shot:last (ultima captura de tela) " +
			"ou shot:<sha256>. Use somente quando o usuario pedir/confirmar anexar; o envio das imagens ao servidor exige " +
			"autorizacao dele no chat. O comentario e criado primeiro; se o upload falhar, o comentario NAO e perdido.",
		Params: []ToolParam{
			{Name: "ticketId", Type: "string", Description: "GUID do chamado", Required: true},
			{Name: "content", Type: "string", Description: "Conteudo do comentario", Required: true},
			{Name: "attachImageRefs", Type: "string", Description: "Opcional: refs das imagens para anexar ao comentario (turn:N, shot:last, shot:<sha256>)", Required: false},
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
			attachRefs := stringOrArrayArg(args, "attachImageRefs")
			if attachRefs == "" {
				return app.AddAgentTicketComment(ticketID, content)
			}
			if err := requireConfirm(args); err != nil {
				return nil, err
			}
			raw, err := app.AddAgentTicketComment(ticketID, content)
			if err != nil {
				return nil, err
			}
			// Daqui em diante o comentario JA EXISTE: qualquer falha de anexo
			// precisa preservar o commentId no resultado.
			commentID := extractJSONID(raw)
			if commentID == "" {
				return map[string]any{
					"ok":       false,
					"ticketId": ticketID,
					"comment":  raw,
					"error":    "comentario criado, mas nao foi possivel extrair o commentId da resposta; imagens nao anexadas",
				}, nil
			}
			images, err := resolveImageRefs(attachRefs, app)
			if err != nil {
				return map[string]any{
					"ok":        false,
					"ticketId":  ticketID,
					"commentId": commentID,
					"comment":   raw,
					"error":     fmt.Sprintf("comentario criado, mas as imagens nao foram anexadas: %v", err),
				}, nil
			}
			ids, err := app.AttachImagesToComment(ticketID, commentID, images)
			if err != nil {
				return map[string]any{
					"ok":          false,
					"ticketId":    ticketID,
					"commentId":   commentID,
					"comment":     raw,
					"attachments": attachmentMetadata(images, ids),
					"error":       fmt.Sprintf("comentario criado, mas o upload das imagens falhou: %v", err),
				}, nil
			}
			return map[string]any{
				"ok":          true,
				"ticketId":    ticketID,
				"commentId":   commentID,
				"comment":     raw,
				"attachments": attachmentMetadata(images, ids),
			}, nil
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
		Name: "create_ticket",
		Description: "ABRE um novo chamado de suporte vinculado a esta maquina. PRÉ-REQUISITO ANTIDUPLICIDADE (OBRIGATORIO): antes de abrir, chame list_tickets e confirme que NAO existe chamado aberto (ClosedAt nulo) sobre o mesmo assunto; se existir, NAO abra um chamado novo — avise o usuario e ofereca complementar o existente com add_ticket_comment. Só abra um chamado novo se o usuario confirmar explicitamente que e um problema diferente. Use SEMPRE que o usuario pedir para abrir ticket, chamado, reportar problema ou solicitar suporte, apos a verificacao. O chamado e automaticamente associado ao agente/maquina. Chame get_agent_info antes para enriquecer o titulo e descricao com dados da maquina. Chame get_department_fields para conhecer os campos personalizados obrigatorios do departamento (eles valem mesmo sem template) e envie os valores em customFields (definitionId->valor). Quando um template foi escolhido, envie templateId + answers (respostas do mini questionario, key->valor). NUNCA oriente o usuario a acessar portais web externos. " +
			"Opcionalmente, attachImageRefs anexa imagens ao chamado criado (turn:N = N-esima imagem que o usuario mandou no chat no ultimo turno, shot:last = ultima captura de tela, shot:<sha256> = captura especifica); use somente quando o usuario pedir/confirmar anexar e o envio ao servidor exige autorizacao dele no chat.",
		Params: []ToolParam{
			{Name: "title", Type: "string", Description: "Titulo do chamado", Required: true},
			{Name: "description", Type: "string", Description: "Descricao detalhada do problema", Required: true},
			{Name: "priority", Type: "integer", Description: "Prioridade: 1=Baixa, 2=Media, 3=Alta, 4=Critica", Required: false},
			{Name: "category", Type: "string", Description: "Categoria do chamado (texto livre). As opcoes disponiveis sao definidas pelo servidor por departamento (formulario de abertura do agent); consulte a lista de departamentos e envie uma categoria coerente com o problema.", Required: false},
			{Name: "templateId", Type: "string", Description: "GUID do template escolhido (opcional; obtenha em list_ticket_templates)", Required: false},
			{Name: "departmentId", Type: "string", Description: "GUID do departamento responsavel (obrigatorio; obtenha em list_departments e escolha o que melhor se enquadra ou pergunte ao usuario)", Required: true},
			{Name: "customFields", Type: "string", Description: "JSON objeto com os valores dos CAMPOS do departamento, mapeando definitionId para valor (opcional)", Required: false},
			{Name: "answers", Type: "string", Description: "JSON objeto com as respostas do mini QUESTIONARIO do template, mapeando a chave da pergunta (key) para o valor (opcional)", Required: false},
			{Name: "attachImageRefs", Type: "string", Description: "Opcional: refs das imagens para anexar ao chamado criado (turn:N, shot:last, shot:<sha256>)", Required: false},
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

			attachRefs := stringOrArrayArg(args, "attachImageRefs")
			if attachRefs == "" {
				return app.CreateAgentTicket(title, description, priority, category, templateID, departmentID, customFieldsJSON, answersJSON)
			}
			if err := requireConfirm(args); err != nil {
				return nil, err
			}
			raw, err := app.CreateAgentTicket(title, description, priority, category, templateID, departmentID, customFieldsJSON, answersJSON)
			if err != nil {
				return nil, err
			}
			// O chamado JA EXISTE: falhas de anexo precisam preservar o ticketId.
			ticketID := extractJSONID(raw)
			if ticketID == "" {
				return map[string]any{
					"ok":     false,
					"ticket": raw,
					"error":  "chamado criado, mas nao foi possivel extrair o ticketId da resposta; imagens nao anexadas",
				}, nil
			}
			images, err := resolveImageRefs(attachRefs, app)
			if err != nil {
				return map[string]any{
					"ok":       false,
					"ticketId": ticketID,
					"ticket":   raw,
					"error":    fmt.Sprintf("chamado criado, mas as imagens nao foram anexadas: %v", err),
				}, nil
			}
			ids, err := app.AttachImagesToTicket(ticketID, images)
			if err != nil {
				return map[string]any{
					"ok":          false,
					"ticketId":    ticketID,
					"ticket":      raw,
					"attachments": attachmentMetadata(images, ids),
					"error":       fmt.Sprintf("chamado criado, mas o upload das imagens falhou: %v", err),
				}, nil
			}
			return map[string]any{
				"ok":          true,
				"ticketId":    ticketID,
				"ticket":      raw,
				"attachments": attachmentMetadata(images, ids),
			}, nil
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
				var value int
				switch v := raw.(type) {
				case float64:
					if float64(int(v)) != v {
						return nil, fmt.Errorf("rating invalido: informe inteiro entre 1 e 5")
					}
					value = int(v)
				case int:
					value = v
				default:
					return nil, fmt.Errorf("rating invalido: informe inteiro entre 1 e 5")
				}
				if value < 1 || value > 5 {
					return nil, fmt.Errorf("rating invalido: informe valor entre 1 e 5")
				}
				rating = &value
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

	// ========== EVENT LOG (CONSOLIDADO) ==========
	registerEventLogTool(reg)

	// ========== DESEMPENHO ==========
	reg.Register(Tool{
		Name:        "get_performance_snapshot",
		Description: "Retorna um snapshot leve e instantaneo de desempenho: uso de CPU (%), memoria (total/usada/livre em GB e %) e uso de disco por volume logico. Prefira em vez de get_inventory para checagens rapidas.",
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return GetPerformanceSnapshot(ctx)
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

	// ========== CAPTURA DE TELA (CONSOLIDADO) ==========
	registerScreenTool(reg, app)
	// ========== FAMILIAS CONSOLIDADAS E NOVAS TOOLS ==========
	// Tools action-based que substituem registrations antigas + tools novas de
	// administracao do Windows. Acoes destrutivas exigem confirm=true.
	registerPrinterTool(reg, app)
	registerNetworkDiagnosticsTool(reg)
	registerProcessControlTool(reg)
	registerOsqueryTool(reg, app)
	registerDiskTool(reg)
	registerServiceControlTool(reg)
	registerSystemInfoTool(reg)
	registerWindowsUpdateTool(reg)
	registerSecurityStatusTool(reg)
	registerScheduledTaskTool(reg)
	registerSharesTool(reg)
	registerPowerActionTool(reg, app)
	registerSendNotificationTool(reg, app)
	// Consulta de SENSORES do agente (somente leitura; nenhum consentimento).
	registerSensorTools(reg, app)
	// Leitura segura do Registro do Windows via guard fail-closed.
	registerRegistryReadTool(reg)
	// Diagnostico corporativo: AD, certificados locais, BitLocker e sessoes.
	registerAdGpoCertsTool(reg)
	// Diagnostico L1 RMM: VPN/Wi-Fi, inicializacao/browser, probe HTTP, Office e perifericos.
	registerRmmDiagnosticsTools(reg)
}

// attachmentMetadata monta a lista de metadados dos anexos devolvida à IA,
// sempre SEM os bytes da imagem.
func attachmentMetadata(images []TicketImageUpload, attachmentIDs []string) []map[string]any {
	out := make([]map[string]any, 0, len(attachmentIDs))
	for i, id := range attachmentIDs {
		item := map[string]any{"attachmentId": id}
		if i < len(images) {
			item["fileName"] = images[i].FileName
			item["contentType"] = images[i].ContentType
			item["sizeBytes"] = imageUploadSizeBytes(images[i])
		}
		out = append(out, item)
	}
	return out
}

// extractJSONID extrai o campo "id" de uma resposta JSON do suporte (chamado
// ou comentário). Devolve "" quando o payload não tem id utilizável.
func extractJSONID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var parsed struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.ID)
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

// requireConfirm valida o marcador interno confirm=true nas tools destrutivas.
// O parâmetro NÃO é exposto no schema ao LLM: o gate de consentimento do chat
// (services/chat.mcpExecuteForChat) injeta confirm=true SOMENTE depois de o
// usuário aprovar a ação. Mantê-lo fora do schema evita que o modelo se
// autoconfirme e reduz o custo de tokens do tools/list.
func stringOrArrayArg(args map[string]any, name string) string {
	raw, ok := args[name]
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(b)
	case []string:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(b)
	default:
		return ""
	}
}

func requireConfirm(args map[string]any) error {
	if v, ok := args["confirm"].(bool); ok && v {
		return nil
	}
	return fmt.Errorf("acao destrutiva sem autorizacao do usuario: o gate de consentimento do chat nao marcou confirm=true")
}
