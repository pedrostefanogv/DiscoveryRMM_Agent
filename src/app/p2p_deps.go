package app

import (
	"discovery/app/p2p"
)

// AppDeps é re-exportado do pacote p2p. A interface foi movida para
// src/app/p2p/deps.go para permitir que o domínio P2P dependa dela sem
// importar o package app (evitando ciclo de importação).
type AppDeps = p2p.AppDeps
