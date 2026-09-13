package p2p

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"

	"github.com/libp2p/go-libp2p/core/crypto"
)

// p2pIdentityDirName / p2pIdentityFileName definem onde a identidade libp2p
// é persistida: <DataDir>/p2p/libp2p-identity.key.
const (
	p2pIdentityDirName  = "p2p"
	p2pIdentityFileName = "libp2p-identity.key"
)

// loadOrCreateLibp2PIdentity carrega (ou cria e persiste) a chave Ed25519 da
// identidade libp2p local.
//
// Correção A5: o host era criado sem libp2p.Identity, gerando um peer.ID novo
// a cada restart do agente. Peers que conheceram o ID antigo recebiam o novo
// como conflito de identidade (RegisterStrict → conflict → BlockPeer append-
// only) — o agente reiniciado ficava permanentemente bloqueado na malha
// (partição de descoberta). Com identidade persistida, o peer.ID sobrevive ao
// restart e o conflito de identidade deixa de ocorrer.
//
// A chave é armazenada em <DataDir>/p2p/libp2p-identity.key (0o600). Chave
// corrompida é regenerada (efeito equivalente ao comportamento antigo:
// peer.ID novo por restart).
func loadOrCreateLibp2PIdentity(dataDir string) (crypto.PrivKey, error) {
	dir := filepath.Join(dataDir, p2pIdentityDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("criar diretorio de identidade p2p: %w", err)
	}
	keyPath := filepath.Join(dir, p2pIdentityFileName)

	if raw, readErr := os.ReadFile(keyPath); readErr == nil && len(raw) > 0 {
		if priv, unmarshalErr := crypto.UnmarshalEd25519PrivateKey(raw); unmarshalErr == nil {
			return priv, nil
		}
		// Chave corrompida/inválida → regenera abaixo.
	}

	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("gerar identidade ed25519: %w", err)
	}
	raw, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("marshal identidade ed25519: %w", err)
	}
	if err := os.WriteFile(keyPath, raw, 0o600); err != nil {
		return nil, fmt.Errorf("persistir identidade ed25519: %w", err)
	}
	return priv, nil
}