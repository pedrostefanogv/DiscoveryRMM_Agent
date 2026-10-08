package mcp

import "context"

const (
	// Descricoes exibidas no pedido de autorizacao de gravacao (chat).
	exportMarkdownDescription = "relatorio de inventario completo em Markdown (.md)"
	exportPDFDescription      = "relatorio de inventario completo em PDF (.pdf)"

	// exportDestinationHint descreve o destino para o usuario decidir com
	// contexto. O caminho exato so e resolvido depois da aprovacao (existe
	// fallback de pasta quando Program Files nao e gravavel).
	exportDestinationHint = "C:\\Program Files\\Discovery\\DiscoveryExports (ou pasta do usuario, quando nao houver permissao)"
)

// exportWithConsent executa uma exportacao SOMENTE depois de o usuario autorizar
// a gravacao (por gravacao, fail-closed). A negativa vira resultado estruturado
// {"approved":false} — e NAO erro — para o LLM informar o usuario em vez de
// repetir a chamada. Erros reais de exportacao sao propagados.
func exportWithConsent(ctx context.Context, app AppBridge, description, format string, run func() (string, error)) (any, error) {
	approved, err := app.RequestFileWriteConsent(ctx, description, exportDestinationHint)
	if err != nil {
		return nil, err
	}
	if !approved {
		return map[string]any{
			"approved": false,
			"format":   format,
			"message":  "Gravacao NAO autorizada pelo usuario. Nao insista: explique o motivo e peca novamente somente se for realmente necessario.",
		}, nil
	}
	path, err := run()
	if err != nil {
		return nil, err
	}
	return map[string]string{"path": path}, nil
}
