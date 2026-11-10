package app

// Ponte entre o erro TIPADO do canal admin (pacote app) e o reconhecedor do
// envelope no pacote core/ai.
//
// core/ai não pode importar app (app importa ai), então o pacote ai expõe um
// resolvedor registrável e app registra aqui a conversão do tipo concreto. Sem
// essa ponte o envelope estruturado seria reconstruído por heurística de texto
// no chat — frágil justamente no caminho que o incidente de 2026-10-17 expôs.

import "discovery/app/core/ai"

func init() {
	ai.RegisterLocalChannelErrorResolver(func(err error) (string, bool) {
		return ipcEnvelopeJSON(err, "")
	})
}
