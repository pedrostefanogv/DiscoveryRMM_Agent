package mcp

import (
	"context"
	"fmt"
)

// registerOpenExternalTools expõe as ações de ABRIR algo no computador do
// usuário: uma pasta no Explorer e um aplicativo já instalado — além da listagem
// de apps instalados (leitura) que evita o LLM adivinhar o nome.
//
// São ações VISÍVEIS na máquina, então entram na política de consentimento por
// ação (ToolConsentFor): o chat pergunta ao usuário ANTES de a tool rodar e a
// negativa vem como approved=false (fail-closed), igual a instalar programa.
func registerOpenExternalTools(reg *Registry, app AppBridge) {
	if reg == nil || app == nil {
		return
	}

	reg.Register(Tool{
		Name: "open_folder",
		Description: "Abre uma pasta no Explorer do Windows. Aceita um apelido conhecido " +
			"(downloads, documentos, desktop, imagens, videos, musicas, temp, perfil, programas) " +
			"ou um caminho absoluto LOCAL existente. Caminho de rede (UNC) e caminho relativo sao recusados. " +
			"A abertura exige AUTORIZACAO do usuario (pergunta exibida no chat ANTES da tool rodar); " +
			"se o usuario negar, o resultado vem com approved=false e a chamada NAO deve ser repetida.",
		Params: []ToolParam{
			{Name: "folder", Type: "string", Description: "Apelido (ex.: downloads) ou caminho absoluto local da pasta.", Required: true},
			{Name: "reason", Type: "string", Description: "Frase curta (mostrada ao usuario) explicando por que abrir a pasta."},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			if ctx != nil && ctx.Err() != nil {
				return nil, fmt.Errorf("abertura cancelada antes de executar")
			}
			return app.OpenFolder(optionalStringArg(args, "folder"), optionalStringArg(args, "reason"))
		},
	})

	reg.Register(Tool{
		Name: "open_app",
		Description: "Abre (inicia) um aplicativo JA INSTALADO no computador. O nome e resolvido em atalhos do " +
			"Menu Iniciar e em App Paths do Registro; quando nao encontra, devolve a lista de nomes parecidos " +
			"(use list_installed_apps para descobrir o nome exato). " +
			"NUNCA e linha de comando: a tool so abre um app existente, sem argumentos. " +
			"A abertura exige AUTORIZACAO do usuario (pergunta exibida no chat ANTES da tool rodar); " +
			"se o usuario negar, o resultado vem com approved=false e a chamada NAO deve ser repetida.",
		Params: []ToolParam{
			{Name: "app", Type: "string", Description: "Nome do aplicativo instalado (ex.: Google Chrome, Notepad++).", Required: true},
			{Name: "reason", Type: "string", Description: "Frase curta (mostrada ao usuario) explicando por que abrir o app."},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			if ctx != nil && ctx.Err() != nil {
				return nil, fmt.Errorf("abertura cancelada antes de executar")
			}
			return app.OpenApp(optionalStringArg(args, "app"), optionalStringArg(args, "reason"))
		},
	})

	reg.Register(Tool{
		Name: "list_installed_apps",
		Description: "Lista os aplicativos instalados (atalhos do Menu Iniciar) para descobrir o NOME exato antes de " +
			"abrir com open_app. Leitura pura: NAO exige autorizacao e nao abre nada. " +
			"Use o campo 'query' para filtrar por parte do nome (ex.: chrome).",
		Params: []ToolParam{
			{Name: "query", Type: "string", Description: "Filtro opcional por parte do nome do aplicativo."},
			{Name: "limit", Type: "integer", Description: "Maximo de nomes (padrao 120, teto 500)."},
		},
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			return app.ListInstalledApps(optionalStringArg(args, "query"), optionalIntArg(args, "limit"))
		},
	})
}
