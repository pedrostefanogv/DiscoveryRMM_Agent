// Package errutil fornece utilitários para tratamento de erros descartados
// intencionalmente (via _ =), garantindo que sejam logados para diagnóstico.
package errutil

import "discovery/internal/logger"

// LogIfErr loga o erro com nível Warn se não for nil.
// Use no lugar de _ = para operações cujo erro não é crítico mas deve ser observável.
//
//	_ = file.Close()        // antes
//	errutil.LogIfErr(file.Close(), "fechar arquivo temporário") // depois
func LogIfErr(err error, context string) {
	if err != nil {
		logger.Warn("erro descartado", "contexto", context, "erro", err.Error())
	}
}

// IgnoreErr descarta o erro explicitamente sem log.
// Use quando o erro é absolutamente esperado e seguro de ignorar.
// Diferente de _, deixa explícita a intenção de ignorar.
func IgnoreErr(_ error) {}
