package mcp

import (
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
	ExportMarkdown() (string, error)
	ExportPDF() (string, error)
	GetOsqueryStatusJSON() (json.RawMessage, error)
	ListInstalled() (string, error)
	GetLogsText() string
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

	reg.Register(Tool{
		Name:        "get_logs",
		Description: "Retorna os logs recentes de operacoes do winget (instalacao, atualizacao, etc).",
		Handler: func(args map[string]any) (any, error) {
			return map[string]string{"logs": app.GetLogsText()}, nil
		},
	})
}
