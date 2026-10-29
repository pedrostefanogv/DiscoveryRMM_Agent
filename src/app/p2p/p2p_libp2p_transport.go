package p2p

// app_p2p_libp2p_transport.go
//
// Protocolos libp2p de transporte P2P para troca de artifacts e gossip entre agents.
// Substitui os endpoints HTTP: /p2p/peers, /p2p/artifact/access,
// /p2p/artifact/{name}, /p2p/artifact/{name}/manifest e /p2p/replicate.
//
// Protocolos definidos (stream-based, JSON framing):
//   /discovery/peers/1.0.0       —" gossip: retorna peers conhecidos + catálogo local
//   /artifact/access/1.0.0       —" emite token de acesso para um artifact
//   /artifact/manifest/1.0.0     —" retorna manifest de chunks de um artifact
//   /artifact/get/1.0.0          —" transfere bytes de um chunk (range-aware)
//   /artifact/replicate/1.0.0    —" notifica peer para pull de artifact (push desativado, retorna Gone)

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"discovery/app/core/platform"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	protoDiscoveryPeers         = "/discovery/peers/1.0.0"
	protoArtifactAccess         = "/artifact/access/1.0.0"
	protoArtifactManifest       = "/artifact/manifest/1.0.0"
	protoArtifactGet            = "/artifact/get/1.0.0"
	protoArtifactReplicate      = "/artifact/replicate/1.0.0"
	protoFetchCandidacy         = "/fetch/candidacy/1.0.0"
	protoFetchHeartbeat         = "/fetch/heartbeat/1.0.0"
	libp2pStreamTimeout         = 30 * time.Second
	libp2pTransferTimeout       = 2 * time.Minute
	libp2pCandidacyTimeout      = 10 * time.Second
	libp2pTransferTimeoutMax    = 4 * time.Hour   // arquivos grandes (6GB+) precisam de deadlines longos
	libp2pManifestTimeout       = 5 * time.Minute // cobre leitura completa do arquivo para gerar manifest
	libp2pMinTransferSpeed      = 512 * 1024      // 512 KB/s - piso conservador para clculo de timeout
	libp2pTransferTimeoutMargin = 30 * time.Second
)

// computeTransferDeadline calcula um deadline adaptativo baseado no tamanho dos dados.
// Piso: libp2pTransferTimeout (2 min). Teto: libp2pTransferTimeoutMax (4 horas).
// Assume velocidade mnima de 512 KB/s mais margem fixa de 30s.
func computeTransferDeadline(dataSize int64) time.Time {
	if dataSize <= 0 {
		return time.Now().Add(libp2pTransferTimeout)
	}
	estimated := time.Duration(dataSize/libp2pMinTransferSpeed)*time.Second + libp2pTransferTimeoutMargin
	if estimated < libp2pTransferTimeout {
		estimated = libp2pTransferTimeout
	}
	if estimated > libp2pTransferTimeoutMax {
		estimated = libp2pTransferTimeoutMax
	}
	return time.Now().Add(estimated)
}

// rollingDeadlineInterval define a cada quantos bytes lidos/escritos o deadline
// do stream é renovado. 512KB equilibra granularidade e custo de SetDeadline.
const rollingDeadlineInterval = 512 * 1024

// rollingDeadlineStreamReader renova o deadline do stream conforme bytes fluem.
// Problema que resolve: com N chunks paralelos sobre a mesma conexão libp2p em
// redes lentas, a vazão por stream cai e o deadline FIXO (piso de 2min) expira
// no meio do chunk, matando todos os streams de uma vez ("Application error
// 0x0" simultâneo em seed e cliente). Com renovação rolante, o timeout passa a
// valer apenas para inatividade real (stall), não para duração total.
type rollingDeadlineStreamReader struct {
	r io.Reader
	// deadlineSetter é satisfeito por network.Stream; interface local permite
	// testar a renovação do deadline sem um stream libp2p real.
	deadlineSetter p2pDeadlineSetter
	interval       int64
	read           int64
	next           int64
}

// p2pDeadlineSetter abstrai SetDeadline do stream.
type p2pDeadlineSetter interface {
	SetDeadline(time.Time) error
}

func (rr *rollingDeadlineStreamReader) Read(p []byte) (int, error) {
	n, err := rr.r.Read(p)
	rr.read += int64(n)
	if rr.read >= rr.next {
		if rr.deadlineSetter != nil {
			_ = rr.deadlineSetter.SetDeadline(time.Now().Add(libp2pTransferTimeout))
		}
		rr.next = rr.read + rr.interval
	}
	return n, err
}

// "—"— Request / Response types "—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—

// libp2pPeersRequest é o frame opcional enviado pelo cliente antes do gossip.
// Peers antigos não enviam nada: o handler trata EOF como "sem credencial".
type libp2pPeersRequest struct {
	RequesterID string `json:"requesterId,omitempty"`
	Token       string `json:"token,omitempty"`
}

// validateDiscoveryAccessToken aplica a política de acesso do gossip de peers.
// O token de acesso é emitido por artifact (P2PArtifactAccess); para o gossip
// aceitamos qualquer token emitido por este agente para o requester informado.
func validateDiscoveryAccessToken(transfer *TransferServer, requesterID, token string) error {
	if transfer == nil {
		return errors.New("servidor de transferência indisponível")
	}
	mode := normalizeP2PArtifactServeMode(transfer.artifactServeMode())
	if strings.TrimSpace(token) == "" {
		if mode == p2pArtifactServeRequire {
			return errors.New("token de acesso obrigatorio: negocie /artifact/access/1.0.0")
		}
		return nil
	}
	if len(token) > maxLibp2pTokenLen {
		return errors.New("token inválido")
	}
	if err := transfer.verifyTokenAny(strings.TrimSpace(requesterID), token, time.Now()); err != nil {
		return fmt.Errorf("token de acesso inválido: %w", err)
	}
	return nil
}

type libp2pPeersResponse struct {
	AgentID       string            `json:"agentId"`
	KnownPeers    []P2PPeerView     `json:"knownPeers"`
	Artifacts     []P2PArtifactView `json:"artifacts"`
	CatalogSource string            `json:"catalogSource"`
	UpdatedAtUTC  string            `json:"updatedAtUtc"`
}

type libp2pAccessRequest struct {
	ArtifactName string `json:"artifactName"`
	RequesterID  string `json:"requesterId"`
}

// libp2pDiscoveryAccess é a resposta de /artifact/access/1.0.0 quando o cliente
// pede o artifact reservado p2pDiscoveryTokenArtifact ("*"). Embute
// P2PArtifactAccess para manter o mesmo shape do protocolo (ArtifactName="*"),
// acrescentando o token em texto: sem URL de download, o token não teria como
// chegar ao cliente.
type libp2pDiscoveryAccess struct {
	P2PArtifactAccess
	Token string `json:"token"`
}

type libp2pManifestRequest struct {
	ArtifactName string `json:"artifactName"`
	RequesterID  string `json:"requesterId"`
	// Token: idem libp2pGetRequest.Token (BearerHMAC de /artifact/access/1.0.0).
	Token string `json:"token,omitempty"`
}

type libp2pGetRequest struct {
	ArtifactName string `json:"artifactName"`
	RequesterID  string `json:"requesterId"`
	RangeStart   int64  `json:"rangeStart"`
	RangeEnd     int64  `json:"rangeEnd"` // -1 = até o fim
	// Token é o BearerHMAC emitido por /artifact/access/1.0.0 (BuildArtifactAccess)
	// ligado ao artifact e ao RequesterID. Sem ele o chunk serve.LEGACY (peers
	// antigos) ou é recusado quando serve.REQUIRE (modelo seguro).
	Token string `json:"token,omitempty"`
}

// maxLibp2pTokenLen limita o tamanho do token aceito em streams libp2p. O token
// legítimo tem ~200 caracteres (payload base64 + HMAC); o teto evita que um peer
// envie payloads gigantes antes do decode.
const maxLibp2pTokenLen = 2048

// validateArtifactAccessToken aplica a política de acesso de artifacts aos
// streams libp2p SERVIDOS por este agente.
//
// Antes desta validação os protocolos /artifact/get, /artifact/manifest e
// /discovery/peers respondiam para QUALQUER peer da LAN sem autenticação: o
// token HMAC emitido por BuildArtifactAccess nunca era conferido do lado que
// serve. Agora:
//
//   - serve.REQUIRE: token obrigatório e válido (artifact + requester).
//   - serve.LEGACY:  sem token => aceita (compatibilidade com agentes antigos);
//     com token => precisa ser válido (impede token errado/adulterado).
//
// Retorna nil quando o acesso é permitido.
func validateArtifactAccessToken(transfer *TransferServer, artifactName, requesterID, token string) error {
	if transfer == nil {
		return errors.New("servidor de transferência indisponível")
	}
	token = strings.TrimSpace(token)
	mode := normalizeP2PArtifactServeMode(transfer.artifactServeMode())
	if token == "" {
		if mode == p2pArtifactServeRequire {
			return errors.New("token de acesso obrigatorio: negocie /artifact/access/1.0.0")
		}
		return nil
	}
	if len(token) > maxLibp2pTokenLen {
		return errors.New("token inválido")
	}
	if err := transfer.verifyToken(artifactName, requesterID, token, time.Now()); err != nil {
		return fmt.Errorf("token de acesso inválido: %w", err)
	}
	return nil
}

// artifactAccessTokenCache guarda o token de acesso emitido por um peer para um
// artifact, evitando um round-trip /artifact/access por chunk (um download de
// 6 GB pode ter centenas de chunks) e por manifest. Renova sozinho antes do
// vencimento.
//
// A chave inclui o artifact porque o token é emitido POR artifact (o HMAC cobre
// o nome): o mesmo peer pode ter tokens diferentes para artifacts diferentes e
// para o gossip (p2pDiscoveryTokenArtifact).
type artifactAccessTokenCache struct {
	mu      sync.Mutex
	entries map[artifactAccessTokenKey]cachedArtifactAccess
}

// artifactAccessTokenKey identifica o token de um peer para um artifact.
// ArtifactName == p2pDiscoveryTokenArtifact identifica o token de discovery.
type artifactAccessTokenKey struct {
	peer     peer.ID
	artifact string
	// requester é parte da chave: o token HMAC é emitido PARA um requesterID e
	// é recusado ("peer nao autorizado") se o requester mudar em runtime — o que
	// acontece no zero-touch, quando o AgentID passa a existir após o onboarding
	// (bug 2026-10-29: tokens velhos rejeitados → falso legacy).
	requester string
}

type cachedArtifactAccess struct {
	token     string
	expiresAt time.Time
}

// accessTokenRenewMargin é a antecedência com que o token é renovado antes do
// vencimento, para não expirar no meio de um chunk.
const accessTokenRenewMargin = 2 * time.Minute

// defaultAccessTokenTTL é usado quando o peer não informa ExpiresAtUTC.
const defaultAccessTokenTTL = 15 * time.Minute

// accessTokenCacheMaxEntries dispara a limpeza de tokens vencidos no cache de
// processo (chave peer × artifact).
const accessTokenCacheMaxEntries = 512

// p2pAccessTokens é o cache compartilhado de tokens de acesso (manifest e
// discovery). O cache de chunks (p2p_chunks.go) continua local a cada download
// para permitir invalidar por tentativa.
var p2pAccessTokens = newArtifactAccessTokenCache()

func newArtifactAccessTokenCache() *artifactAccessTokenCache {
	return &artifactAccessTokenCache{entries: make(map[artifactAccessTokenKey]cachedArtifactAccess)}
}

// cached devolve o token memorizado quando ainda está longe do vencimento.
func (c *artifactAccessTokenCache) cached(peerID peer.ID, artifactName, requesterID string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[artifactAccessTokenKey{peer: peerID, artifact: artifactName, requester: requesterID}]
	if !ok || entry.token == "" {
		return "", false
	}
	if !time.Now().Add(accessTokenRenewMargin).Before(entry.expiresAt) {
		return "", false
	}
	return entry.token, true
}

// store memoriza o token (ignora token vazio: nada a reutilizar).
func (c *artifactAccessTokenCache) store(peerID peer.ID, artifactName, requesterID, token string, expiresAt time.Time) {
	if c == nil || strings.TrimSpace(token) == "" {
		return
	}
	c.mu.Lock()
	c.entries[artifactAccessTokenKey{peer: peerID, artifact: artifactName, requester: requesterID}] = cachedArtifactAccess{token: token, expiresAt: expiresAt}
	// O cache de processo cresce com (peer × artifact): descarta vencidos quando
	// fica grande em vez de crescer indefinidamente.
	if len(c.entries) > accessTokenCacheMaxEntries {
		now := time.Now()
		for key, entry := range c.entries {
			if now.After(entry.expiresAt) {
				delete(c.entries, key)
			}
		}
	}
	c.mu.Unlock()
}

// accessTokenExpiry normaliza o vencimento informado pelo peer.
func accessTokenExpiry(access P2PArtifactAccess) time.Time {
	if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(access.ExpiresAtUTC)); err == nil {
		return parsed
	}
	return time.Now().Add(defaultAccessTokenTTL)
}

// token devolve o token do peer, negociando quando ausente/expirado. Falha de
// negociação não é fatal aqui: devolve "" e o servidor decide conforme o modo
// (segue no "legacy" ou recusa no "require" — o erro aparece no chunk).
func (c *artifactAccessTokenCache) token(ctx context.Context, h host.Host, peerID peer.ID, artifactName, requesterID string, logf func(string)) string {
	if c == nil {
		return ""
	}
	if token, ok := c.cached(peerID, artifactName, requesterID); ok {
		return token
	}
	access, err := libp2pRequestAccess(ctx, h, peerID, artifactName, requesterID)
	if err != nil {
		if logf != nil {
			logf(fmt.Sprintf("[p2p][access] negociação de token falhou para artifact=%s peer=%s: %v",
				artifactName, peerID.String(), err))
		}
		return ""
	}
	token := tokenFromAccessURL(access.URL)
	c.store(peerID, artifactName, requesterID, token, accessTokenExpiry(access))
	return token
}

// discoveryToken negocia (com cache) o token de discovery ("*"), usado no
// gossip de peers. Devolve o erro de negociação para que o caller identifique
// peers antigos (que não conhecem "*") e caia no modo legacy explicitamente.
func (c *artifactAccessTokenCache) discoveryToken(ctx context.Context, h host.Host, peerID peer.ID, requesterID string, logf func(string)) (string, error) {
	if c == nil {
		return "", errors.New("cache de tokens de discovery indisponível")
	}
	if token, ok := c.cached(peerID, p2pDiscoveryTokenArtifact, requesterID); ok {
		return token, nil
	}
	access, err := libp2pRequestDiscoveryAccess(ctx, h, peerID, requesterID)
	if err != nil {
		if logf != nil {
			logf(fmt.Sprintf("[p2p][access] negociação do token de discovery falhou peer=%s: %v",
				peerID.String(), err))
		}
		return "", err
	}
	token := strings.TrimSpace(access.Token)
	if token == "" {
		return "", errors.New("peer devolveu token de discovery vazio")
	}
	c.store(peerID, p2pDiscoveryTokenArtifact, requesterID, token, accessTokenExpiry(access.P2PArtifactAccess))
	return token, nil
}

// invalidate remove TODOS os tokens cacheados do peer (ex.: token
// expirado/adulterado/rejeitado), em qualquer artifact.
func (c *artifactAccessTokenCache) invalidate(peerID peer.ID) {
	if c == nil {
		return
	}
	c.mu.Lock()
	for key := range c.entries {
		if key.peer == peerID {
			delete(c.entries, key)
		}
	}
	c.mu.Unlock()
}

// tokenFromAccessURL extrai o token do campo URL devolvido pelo handler
// /artifact/access (formato .../p2p/artifact/<nome>?peer=<id>&token=<token>).
func tokenFromAccessURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return u.Query().Get("token")
}

// errChunkChecksumMismatch marca divergência entre o SHA256 do chunk recebido e
// o do manifest. Assinatura de manifest stale (peer servindo uma versão antiga
// do artifact) ou de corrupção — usada para invalidar o peer imediatamente.
var errChunkChecksumMismatch = errors.New("checksum divergente do chunk")

// errFinalChecksumMismatch marca divergência no SHA256 do arquivo montado a
// partir dos chunks (manifest stale com o mesmo nome/tamanho).
var errFinalChecksumMismatch = errors.New("checksum do arquivo final divergente")

// isStaleManifestError indica que a falha do download aponta manifest stale. A
// detecção usa sentinelas (errors.Is) e mantém um fallback por mensagem para
// caminhos que ainda não embrulham os sentinelas.
func isStaleManifestError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errChunkChecksumMismatch) || errors.Is(err, errFinalChecksumMismatch) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "checksum divergente") ||
		strings.Contains(msg, "checksum do arquivo final divergente")
}

// isArtifactAccessAuthError indica que a falha do chunk veio de credencial
// (token ausente/inválido/expirado) e não de rede/manifest.
func isArtifactAccessAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "token de acesso") || strings.Contains(msg, "token inválido") ||
		strings.Contains(msg, "token expirado") || strings.Contains(msg, "assinatura invalida")
}

type libp2pGetResponse struct {
	ArtifactName string `json:"artifactName"`
	SHA256       string `json:"sha256"`
	TotalSize    int64  `json:"totalSize"`
	RangeStart   int64  `json:"rangeStart"`
	RangeEnd     int64  `json:"rangeEnd"`
	// Após este JSON header, o remetente envia exatamente (RangeEnd-RangeStart+1) bytes.
}

type libp2pReplicateRequest struct {
	ArtifactName   string `json:"artifactName"`
	ChecksumSHA256 string `json:"checksumSha256"`
	SourceAgentID  string `json:"sourceAgentId"`
}

type libp2pReplicateResponse struct {
	Gone    bool   `json:"gone"`
	Message string `json:"message"`
}

type libp2pErrorResponse struct {
	Error string `json:"error"`
	// Code classifica o erro de forma estável (não depende do texto), usado para
	// distinguir "peer antigo não suporta o protocolo" de "credencial rejeitada"
	// — antes a heurística casava "peer: " e tratava QUALQUER rejeição como peer
	// legacy (bug 2026-10-29, apontado na revisão).
	Code string `json:"code,omitempty"`
}

// Códigos estáveis de erro do protocolo libp2p.
const (
	p2pErrCodeUnsupported = "unsupported" // peer não conhece o protocolo/artifact (legacy)
	p2pErrCodeAuth        = "auth"        // token ausente/inválido/expirado
	p2pErrCodeNotFound    = "not_found"   // artifact inexistente
	p2pErrCodeInvalid     = "invalid"     // payload inválido
)

// peerStreamError converte a resposta de erro do peer em error de cliente,
// preservando o código estável (quando o peer o envia) para que os chamadores
// distingam auth de "protocolo não suportado" sem heurística de texto.
func peerStreamError(errResp libp2pErrorResponse) error {
	msg := strings.TrimSpace(errResp.Error)
	code := strings.TrimSpace(errResp.Code)
	if code == "" {
		return fmt.Errorf("peer: %s", msg)
	}
	return fmt.Errorf("peer: %s (código %s)", msg, code)
}

// "—"— Tipos do protocolo /fetch "—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—

// libp2pCandidacyRequest é enviado por um peer que quer se candidatar a fetcher.
// Reutiliza ArtifactFetchCandidate como payload.
type libp2pCandidacyRequest = ArtifactFetchCandidate

// libp2pCandidacyResponse confirma recebimento da candidatura.
type libp2pCandidacyResponse struct {
	Accepted bool   `json:"accepted"`
	Message  string `json:"message,omitempty"`
}

// "—"— Server-side: registrar handlers no host libp2p "—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—

// RegisterP2PProtocols instala todos os stream handlers nos 7 protocolos.
// Chamado em app_p2p_libp2p.go após criar o host.
func RegisterP2PProtocols(h host.Host, coord *Coordinator, transfer *TransferServer) {
	h.SetStreamHandler(protoDiscoveryPeers, func(s network.Stream) {
		handleStreamPeers(s, coord, transfer)
	})
	h.SetStreamHandler(protoArtifactAccess, func(s network.Stream) {
		handleStreamArtifactAccess(s, transfer)
	})
	h.SetStreamHandler(protoArtifactManifest, func(s network.Stream) {
		handleStreamArtifactManifest(s, transfer)
	})
	h.SetStreamHandler(protoArtifactGet, func(s network.Stream) {
		handleStreamArtifactGet(s, transfer)
	})
	h.SetStreamHandler(protoArtifactReplicate, func(s network.Stream) {
		handleStreamArtifactReplicate(s)
	})
	h.SetStreamHandler(protoFetchCandidacy, func(s network.Stream) {
		handleStreamFetchCandidacy(s, coord)
	})
	h.SetStreamHandler(protoFetchHeartbeat, func(s network.Stream) {
		handleStreamFetchHeartbeat(s, coord)
	})
}

func handleStreamPeers(s network.Stream, coord *Coordinator, transfer *TransferServer) {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(libp2pStreamTimeout))

	// O gossip de peers não tem artifact associado; ainda assim, quando o peer
	// envia credencial ela precisa ser válida (defesa contra catálogo servido a
	// quem reaproveita token adulterado) e, no modo REQUIRE, é obrigatória.
	req := libp2pPeersRequest{}
	if err := json.NewDecoder(bufio.NewReader(s)).Decode(&req); err != nil && err != io.EOF {
		req = libp2pPeersRequest{} // frame inválido do requester = sem credencial
	}
	if err := validateDiscoveryAccessToken(transfer, strings.TrimSpace(req.RequesterID), strings.TrimSpace(req.Token)); err != nil {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: err.Error(), Code: p2pErrCodeAuth})
		return
	}

	enc := json.NewEncoder(s)
	peers := coord.GetPeers()
	artifacts := transfer.localArtifactsSnapshot()
	transfer.mu.RLock()
	agentID := transfer.agentID
	transfer.mu.RUnlock()

	_ = enc.Encode(libp2pPeersResponse{
		AgentID:       agentID,
		KnownPeers:    peers,
		Artifacts:     artifacts,
		CatalogSource: "self",
		UpdatedAtUTC:  time.Now().UTC().Format(time.RFC3339),
	})
}

func handleStreamArtifactAccess(s network.Stream, transfer *TransferServer) {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(libp2pStreamTimeout))

	var req libp2pAccessRequest
	if err := json.NewDecoder(bufio.NewReader(s)).Decode(&req); err != nil {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "payload inválido"})
		return
	}
	req.ArtifactName = SanitizeArtifactName(req.ArtifactName)
	req.RequesterID = strings.TrimSpace(req.RequesterID)
	if req.ArtifactName == "" {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "artifact inválido"})
		return
	}
	if req.RequesterID == "" {
		req.RequesterID = "peer-anon"
	}
	// Token de discovery ("*"): credencia apenas o gossip de peers. Não exige
	// que nenhum arquivo exista — antes disso o cliente nunca conseguia
	// autenticar em /discovery/peers no modo "require" (catálogo sempre vazio).
	if req.ArtifactName == p2pDiscoveryTokenArtifact {
		access, token, err := transfer.BuildDiscoveryAccess(req.RequesterID)
		if err != nil {
			_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: err.Error()})
			return
		}
		_ = json.NewEncoder(s).Encode(libp2pDiscoveryAccess{P2PArtifactAccess: access, Token: token})
		return
	}
	access, err := transfer.BuildArtifactAccess(req.ArtifactName, req.RequesterID)
	if err != nil {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: err.Error()})
		return
	}
	_ = json.NewEncoder(s).Encode(access)
}

func handleStreamArtifactManifest(s network.Stream, transfer *TransferServer) {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(libp2pStreamTimeout))

	var req libp2pManifestRequest
	if err := json.NewDecoder(bufio.NewReader(s)).Decode(&req); err != nil {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "payload inválido"})
		return
	}
	req.ArtifactName = SanitizeArtifactName(req.ArtifactName)
	req.RequesterID = strings.TrimSpace(req.RequesterID)
	if req.ArtifactName == "" {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "artifact inválido"})
		return
	}
	if err := validateArtifactAccessToken(transfer, req.ArtifactName, req.RequesterID, req.Token); err != nil {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: err.Error(), Code: p2pErrCodeAuth})
		return
	}
	transfer.mu.RLock()
	tempDir := transfer.tempDir
	deps := transfer.deps
	transfer.mu.RUnlock()

	// Rejeitar arquivos .importing (ainda sendo copiados), .partial (montagem
	// em andamento) e sidecars .meta — não devem gerar nem servir manifest,
	// assim como o /artifact/get rejeita. Evita servir um manifest com
	// offsets/hashes de um arquivo parcial.
	if strings.HasSuffix(req.ArtifactName, ".importing") ||
		strings.HasSuffix(req.ArtifactName, ".partial") ||
		strings.HasSuffix(req.ArtifactName, ".meta") ||
		strings.HasSuffix(req.ArtifactName, ".meta.json") {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "artifact em andamento"})
		return
	}

	path := filepath.Join(tempDir, req.ArtifactName)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "artifact no encontrado"})
		return
	}

	// Tentar cache de manifest primeiro. Valida com manifestMatchesFile para
	// garantir que o manifest corresponde ao arquivo atual em disco (tamanho +
	// mtime + hash dos chunks de borda). Sem essa validação, um artifact
	// republicado com o mesmo nome serviria um manifest stale com offsets/hashes
	// da versão antiga, fazendo o receiver validar chunks contra hashes errados
	// (checksum divergente).
	manifestDir := filepath.Join(tempDir, manifestDirName)
	if cached := loadCachedManifest(manifestDir, req.ArtifactName, path); cached != nil {
		if manifestMatchesFile(cached, path) {
			if transfer != nil && transfer.coord != nil && transfer.coord.deps != nil {
				shaShort := cached.SHA256
				if len(shaShort) > 16 {
					shaShort = shaShort[:16]
				}
				transfer.coord.deps.Log(fmt.Sprintf(
					"[p2p][serve-manifest] cache hit artifact=%s chunks=%d size=%d mtime=%d sha256=%s",
					req.ArtifactName, cached.TotalChunks, cached.TotalSize, cached.SourceMTime, shaShort))
			}
			_ = json.NewEncoder(s).Encode(cached)
			return
		}
		// Cache stale: invalida e regenera abaixo.
		if transfer != nil && transfer.coord != nil && transfer.coord.deps != nil {
			transfer.coord.deps.Log(fmt.Sprintf(
				"[p2p][serve-manifest] cache stale artifact=%s — invalidando e regenerando", req.ArtifactName))
		}
		if transfer != nil && transfer.coord != nil {
			transfer.coord.recordStaleManifest()
		}
		_ = os.Remove(cachedManifestPath(manifestDir, req.ArtifactName))
	}

	// Reajustar deadline: buildChunkManifest l o arquivo inteiro.
	_ = s.SetDeadline(computeTransferDeadline(info.Size()))

	var chunkSize int64 = defaultChunkSizeBytes
	if deps != nil {
		if cfg := deps.GetP2PConfig(); cfg.ChunkSizeBytes > 0 {
			chunkSize = cfg.ChunkSizeBytes
		}
	}
	var cpuFn func() float64
	if transfer != nil && transfer.coord != nil {
		cpuFn = func() float64 { return transfer.coord.cpuSampler.Sample() }
	}

	// Geração com dedup (manifestInFlight) e contexto de vida do agente: evita
	// N leituras completas do mesmo arquivo quando vários peers pedem o manifest
	// juntos e permite abortar no shutdown.
	buildCtx := context.Background()
	useCoord := transfer != nil && transfer.coord != nil
	var manifest P2PChunkManifest
	if useCoord {
		buildCtx = transfer.coord.Ctx()
		manifest, err = transfer.coord.ensureServingManifest(buildCtx, path, req.ArtifactName, chunkSize, cpuFn)
	} else {
		manifest, err = buildChunkManifest(buildCtx, path, CanonicalArtifactID("", req.ArtifactName, ""), chunkSize, nil, cpuFn)
	}
	if err != nil {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "erro ao construir manifest: " + err.Error()})
		return
	}
	if !useCoord {
		// Fallback sem coordinator: cacheia o manifest para reuso futuro.
		_ = saveCachedManifest(manifestDir, req.ArtifactName, manifest)
	}

	_ = json.NewEncoder(s).Encode(manifest)
}

func handleStreamArtifactGet(s network.Stream, transfer *TransferServer) {
	defer s.Close()

	var req libp2pGetRequest
	if err := json.NewDecoder(bufio.NewReader(s)).Decode(&req); err != nil {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "payload inválido"})
		return
	}
	req.ArtifactName = SanitizeArtifactName(req.ArtifactName)
	req.RequesterID = strings.TrimSpace(req.RequesterID)
	if req.ArtifactName == "" {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "artifact inválido"})
		return
	}
	if err := validateArtifactAccessToken(transfer, req.ArtifactName, req.RequesterID, req.Token); err != nil {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: err.Error(), Code: p2pErrCodeAuth})
		return
	}
	transfer.mu.RLock()
	tempDir := transfer.tempDir
	transfer.mu.RUnlock()

	// Rejeitar arquivos .importing (ainda sendo copiados) - no devem ser servidos.
	// B12: também rejeita .partial (montagem em andamento — o peer consumiria
	// um arquivo incompleto) e os sidecars .meta/.meta.json (metadados internos,
	// não são payload para transferência).
	if strings.HasSuffix(req.ArtifactName, ".importing") ||
		strings.HasSuffix(req.ArtifactName, ".partial") ||
		strings.HasSuffix(req.ArtifactName, ".meta") ||
		strings.HasSuffix(req.ArtifactName, ".meta.json") {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "artifact em andamento"})
		return
	}

	path := filepath.Join(tempDir, req.ArtifactName)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "artifact no encontrado"})
		return
	}

	// Reusa SHA256 do manifest cacheado em vez de recalcular o arquivo inteiro.
	// IMPORTANTE: valida o manifest contra o arquivo atual (tamanho + mtime +
	// hash de chunks de borda) antes de servir. Sem isso, um arquivo substituído
	// (republicado) durante transferências em andamento faria o servidor servir
	// bytes da versão nova enquanto o cliente valida contra hashes da versão
	// antiga → checksum divergente em chunks aleatórios.
	checksum := ""
	if transfer != nil {
		manifestDir := transfer.manifestDir()
		if cached := loadCachedManifest(manifestDir, req.ArtifactName, path); cached != nil {
			if manifestMatchesFile(cached, path) {
				checksum = cached.SHA256
			} else {
				// Manifest stale: invalida para forçar regeneração no próximo
				// serve-manifest e recalcula o checksum do arquivo atual.
				if transfer.coord != nil && transfer.coord.deps != nil {
					transfer.coord.deps.Log(fmt.Sprintf(
						"[p2p][serve] manifest stale detectado no GET artifact=%s (mtime arquivo=%d) — invalidando cache",
						req.ArtifactName, info.ModTime().UnixNano()))
					transfer.coord.recordStaleManifest()
				}
				_ = os.Remove(cachedManifestPath(manifestDir, req.ArtifactName))
			}
		}
	}
	if checksum == "" {
		var csErr error
		checksum, csErr = computeFileSHA256(path)
		if csErr != nil {
			_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "erro ao calcular checksum"})
			return
		}
	}

	totalSize := info.Size()
	rangeStart := req.RangeStart
	rangeEnd := req.RangeEnd
	if rangeStart < 0 {
		rangeStart = 0
	}
	if rangeEnd < 0 || rangeEnd >= totalSize {
		rangeEnd = totalSize - 1
	}
	if rangeStart > rangeEnd {
		_ = json.NewEncoder(s).Encode(libp2pErrorResponse{Error: "range inválido"})
		return
	}
	// Diagnóstico: se o range solicitado difere do que será servido, é forte
	// indício de manifest stale (o cliente pediu offsets de uma versão antiga
	// do artifact). Loga para facilitar a depuração de checksum divergente.
	if req.RangeEnd != rangeEnd || req.RangeStart != rangeStart {
		if transfer != nil && transfer.coord != nil && transfer.coord.deps != nil {
			transfer.coord.deps.Log(fmt.Sprintf(
				"[p2p][serve] range ajustado artifact=%s solicitado=%d-%d servido=%d-%d (totalSize=%d) — possível manifest stale",
				req.ArtifactName, req.RangeStart, req.RangeEnd, rangeStart, rangeEnd, totalSize))
		}
	}
	chunkLen := rangeEnd - rangeStart + 1

	// Deadline adaptativo baseado no tamanho real a transferir.
	_ = s.SetDeadline(computeTransferDeadline(chunkLen))

	// Enviar header JSON primeiro.
	hdr := libp2pGetResponse{
		ArtifactName: req.ArtifactName,
		SHA256:       checksum,
		TotalSize:    totalSize,
		RangeStart:   rangeStart,
		RangeEnd:     rangeEnd,
	}
	if err := json.NewEncoder(s).Encode(hdr); err != nil {
		return
	}

	// Stream de bytes.
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if rangeStart > 0 {
		if _, err := f.Seek(rangeStart, io.SeekStart); err != nil {
			return
		}
	}

	var coord *Coordinator
	if transfer != nil {
		coord = transfer.coord
	}
	requesterID := strings.TrimSpace(req.RequesterID)
	if requesterID == "" {
		requesterID = "peer-remote"
	}

	reader := io.LimitReader(f, chunkLen)
	if coord != nil {
		// Track progresso acumulado por sesso (artifact+requester) para
		// que a barra seja monotnica e relativa ao tamanho total do arquivo,
		// no a cada chunk individual.
		sessionKey := req.ArtifactName + "|" + requesterID
		coord.servingSessionsMu.Lock()
		sess, exists := coord.servingSessions[sessionKey]
		if !exists {
			sess = &servingSession{
				artifactName: req.ArtifactName,
				requesterID:  requesterID,
				totalSize:    totalSize,
				lastActive:   time.Now(),
			}
			coord.servingSessions[sessionKey] = sess
		}
		sess.lastActive = time.Now()
		coord.servingSessionsMu.Unlock()

		reader = newProgressReader(reader, chunkLen, func(readSoFar int64) {
			coord.servingSessionsMu.Lock()
			// Atualiza lastActive para que o GC não remova a sessão durante
			// um upload longo (ex.: arquivos grandes em redes lentas).
			sess.lastActive = time.Now()
			accumulated := sess.servedBytes + readSoFar
			// S emite se houve mudana significativa (>1% ou 100KB)
			delta := accumulated - sess.lastReported
			threshold := sess.totalSize / 100
			if threshold < 100*1024 {
				threshold = 100 * 1024
			}
			if delta >= threshold || accumulated >= sess.totalSize {
				sess.lastReported = accumulated
				coord.servingSessionsMu.Unlock()
				coord.emitTransferProgress(p2pTransferProgress{
					ArtifactName: req.ArtifactName,
					PeerID:       requesterID,
					BytesRead:    accumulated,
					TotalBytes:   sess.totalSize,
					Operation:    "serve",
					Direction:    "upload",
				})
				return
			}
			coord.servingSessionsMu.Unlock()
		})
	}

	// Deadline para o lado servidor: evita que o libp2p encerre a conexo
	// por timeout enquanto o chunk est sendo enviado (especialmente em redes lentas).
	// Deadline ROLANTE: renovado a cada 512KB enviados — só expira em stall real,
	// não em transferência lenta (vazão baixa com chunks paralelos).
	_ = s.SetDeadline(computeTransferDeadline(chunkLen))
	reader = &rollingDeadlineStreamReader{r: reader, deadlineSetter: s, interval: rollingDeadlineInterval}
	// M15: throttle de bandwidth no sender (bytes servidos a peers) — bucket
	// global da máquina, configurável via servidor.
	if bwMaxPerSec.Load() > 0 {
		// Usa o contexto de vida do agente (não Background): o wait do token
		// bucket passa a ser cancelável no shutdown em vez de segurar a
		// goroutine até o fim do sleep. O deadline rolante do stream continua
		// sendo o limite para peer que para de ler.
		throttleCtx := context.Background()
		if coord != nil && coord.deps != nil {
			if appCtx := coord.deps.Context(); appCtx != nil {
				throttleCtx = appCtx
			}
		}
		reader = newBandwidthThrottleReader(reader, throttleCtx)
	}

	written, copyErr := io.Copy(s, reader)
	if coord != nil && written > 0 {
		coord.recordBytesServed(written)
	}
	if copyErr != nil && coord != nil && coord.deps != nil {
		coord.deps.Log(fmt.Sprintf("[p2p][serve] erro ao enviar chunk artifact=%s para peer=%s chunkLen=%d enviado=%d: %v",
			req.ArtifactName, requesterID, chunkLen, written, copyErr))
	}
	if coord != nil {
		// Atualiza a sesso com os bytes acumulados desse chunk.
		sessionKey := req.ArtifactName + "|" + requesterID
		coord.servingSessionsMu.Lock()
		sess, ok := coord.servingSessions[sessionKey]
		// Snapshot dos valores DENTRO do lock para evitar data race: a sessão
		// pode ser deletada por outra goroutine (mesmo artifact+requester) logo
		// após o Unlock, então não podemos ler sess.servedBytes/totalSize fora.
		var snapshotServedBytes, snapshotTotalSize int64
		if ok {
			sess.servedBytes += written
			sess.lastReported = sess.servedBytes
			snapshotServedBytes = sess.servedBytes
			snapshotTotalSize = sess.totalSize
		}
		isComplete := ok && sess.servedBytes >= sess.totalSize
		// Se deu erro ou a sesso no existe, tambm encerra.
		isDone := copyErr != nil || isComplete || !ok
		if isDone {
			delete(coord.servingSessions, sessionKey)
		}
		coord.servingSessionsMu.Unlock()

		done := p2pTransferProgress{
			ArtifactName: req.ArtifactName,
			PeerID:       requesterID,
			Operation:    "serve",
			Direction:    "upload",
		}
		// BytesRead/TotalBytes so sempre relativos ao arquivo completo
		// para uma barra de progresso monotnica.
		if ok {
			done.BytesRead = snapshotServedBytes
			done.TotalBytes = snapshotTotalSize
		} else {
			done.BytesRead = written
			done.TotalBytes = chunkLen
		}
		done.Done = isDone
		if copyErr != nil {
			done.Error = copyErr.Error()
		}

		coord.emitTransferProgress(done)
	}
}

func handleStreamArtifactReplicate(s network.Stream) {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(libp2pStreamTimeout))
	_ = json.NewDecoder(bufio.NewReader(s)).Decode(&struct{}{}) // consume request
	_ = json.NewEncoder(s).Encode(libp2pReplicateResponse{
		Gone:    true,
		Message: "modo push desabilitado: use transferencia pull sob demanda",
	})
}

// "—"— Client-side: funções de chamada para peers "—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—"—

// libp2pFetchPeers abre um stream /discovery/peers/1.0.0 para o peer, envia o
// frame libp2pPeersRequest (RequesterID + token de discovery) e retorna a
// resposta de gossip (catálogo + peers conhecidos).
//
// O frame é OBRIGATÓRIO a partir do momento em que o servidor passou a exigir
// token no modo "require": sem ele o catálogo vinha sempre vazio. Peers antigos
// simplesmente ignoram o frame (não leem do stream) e respondem no mesmo stream.
func libp2pFetchPeers(ctx context.Context, h host.Host, peerID peer.ID, requesterID, token string) (libp2pPeersResponse, error) {
	s, err := h.NewStream(ctx, peerID, protoDiscoveryPeers)
	if err != nil {
		return libp2pPeersResponse{}, fmt.Errorf("stream peers: %w", err)
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(libp2pStreamTimeout))

	req := libp2pPeersRequest{
		RequesterID: strings.TrimSpace(requesterID),
		Token:       strings.TrimSpace(token),
	}
	if err := json.NewEncoder(s).Encode(req); err != nil {
		return libp2pPeersResponse{}, fmt.Errorf("encode peers req: %w", err)
	}

	// O servidor pode responder libp2pErrorResponse (token ausente/inválido,
	// modo require). Lê tudo e diferencia erro de resposta — sem isso o JSON de
	// erro decodificava como "peers vazio" e a causa real (permissão) se perdia.
	raw, err := io.ReadAll(bufio.NewReader(s))
	if err != nil {
		return libp2pPeersResponse{}, fmt.Errorf("read peers resp: %w", err)
	}
	var errResp libp2pErrorResponse
	if json.Unmarshal(raw, &errResp) == nil && strings.TrimSpace(errResp.Error) != "" {
		return libp2pPeersResponse{}, peerStreamError(errResp)
	}
	var resp libp2pPeersResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return libp2pPeersResponse{}, fmt.Errorf("decode peers: %w", err)
	}
	return resp, nil
}

// discoveryLegacyPeerTTL é por quanto tempo um peer identificado como "legacy"
// (não conhece o token de discovery) fica marcado antes de tentarmos de novo.
// Evita um stream por ciclo de gossip (45s) contra um peer que nunca vai
// responder o token — mas permite que um peer atualizado volte a funcionar.
const discoveryLegacyPeerTTL = 10 * time.Minute

// discoveryLegacyCache marca peers antigos (protocolo sem token de discovery).
type discoveryLegacyCache struct {
	mu    sync.Mutex
	until map[peer.ID]time.Time
}

var p2pDiscoveryLegacyPeers = &discoveryLegacyCache{until: make(map[peer.ID]time.Time)}

// isLegacy informa se o peer está marcado como legacy e a marca ainda vale.
func (c *discoveryLegacyCache) isLegacy(peerID peer.ID) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	until, ok := c.until[peerID]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		delete(c.until, peerID)
		return false
	}
	return true
}

// markLegacy marca o peer como legacy. Retorna true somente na primeira vez
// dentro da janela (permite logar uma única vez, sem loop de erro).
func (c *discoveryLegacyCache) markLegacy(peerID peer.ID) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if until, ok := c.until[peerID]; ok && time.Now().Before(until) {
		return false
	}
	c.until[peerID] = time.Now().Add(discoveryLegacyPeerTTL)
	return true
}

// isLegacyDiscoveryPeerError indica que o peer respondeu um rejeição de
// protocolo (não conhece o artifact "*") em vez de erro de rede. Só respostas
// de erro do handler (prefixo "peer: ") marcam o peer como legacy: um timeout
// não pode congelar o catálogo por 10 minutos.
func isLegacyDiscoveryPeerError(err error) bool {
	if err == nil {
		return false
	}
	// Só é "legacy" quando o peer REJEITOU o artifact reservado "*" por não
	// conhecê-lo (comportamento de binário antigo). Erro de credencial, timeout
	// ou assinatura NÃO pode congelar o catálogo por 10 min: antes a heurística
	// incluía "peer: " e casava qualquer resposta de erro do servidor.
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "código unsupported") || strings.Contains(msg, "codigo unsupported") {
		return true
	}
	return strings.Contains(msg, "artifact inválido") || strings.Contains(msg, "artifact invalido") ||
		strings.Contains(msg, "artifact não encontrado") || strings.Contains(msg, "artifact no encontrado")
}

// fetchPeerGossip faz o gossip autenticado de peers: negocia o token de
// discovery (cacheado por peer), envia o frame e — em caso de token rejeitado —
// renova e tenta uma vez. Peers antigos (sem suporte a "*") são marcados como
// legacy e o catálogo remoto fica vazio, com log único.
func fetchPeerGossip(ctx context.Context, h host.Host, peerID peer.ID, requesterID string, logf func(string)) (libp2pPeersResponse, error) {
	requesterID = strings.TrimSpace(requesterID)
	if requesterID == "" {
		requesterID = "peer-local"
	}
	if p2pDiscoveryLegacyPeers.isLegacy(peerID) {
		return libp2pPeersResponse{}, fmt.Errorf("peer %s em modo legacy: gossip autenticado indisponível", peerID.String())
	}

	token, err := p2pAccessTokens.discoveryToken(ctx, h, peerID, requesterID, logf)
	if err != nil {
		// ORDEM IMPORTA (bug 2026-10-29): auth ANTES de legacy. Todo erro do
		// servidor chega como "peer: ..."; classificar legacy primeiro marcava
		// o peer como "antigo" por 10 min após uma simples rejeição de token
		// (e o secret do servidor é regenerado a cada restart do peer!).
		if isArtifactAccessAuthError(err) {
			p2pAccessTokens.invalidate(peerID)
			if token2, terr := p2pAccessTokens.discoveryToken(ctx, h, peerID, requesterID, logf); terr == nil {
				token = token2
				err = nil
			}
		}
		if err != nil {
			if isLegacyDiscoveryPeerError(err) && p2pDiscoveryLegacyPeers.markLegacy(peerID) && logf != nil {
				logf(fmt.Sprintf("[p2p][gossip] peer %s não suporta token de discovery (agente antigo): catálogo remoto desabilitado por %s",
					peerID.String(), discoveryLegacyPeerTTL))
			}
			return libp2pPeersResponse{}, err
		}
	}

	resp, err := libp2pFetchPeers(ctx, h, peerID, requesterID, token)
	if err == nil {
		return resp, nil
	}
	if isArtifactAccessAuthError(err) {
		// Token morreu no meio (expirou/rotacionou): invalida, renova e tenta 1x.
		p2pAccessTokens.invalidate(peerID)
		token2, terr := p2pAccessTokens.discoveryToken(ctx, h, peerID, requesterID, logf)
		if terr == nil {
			if resp2, err2 := libp2pFetchPeers(ctx, h, peerID, requesterID, token2); err2 == nil {
				return resp2, nil
			} else {
				err = err2
			}
		}
	}
	if isLegacyDiscoveryPeerError(err) {
		if p2pDiscoveryLegacyPeers.markLegacy(peerID) && logf != nil {
			logf(fmt.Sprintf("[p2p][gossip] peer %s rejeitou o token de discovery (agente antigo): catálogo remoto desabilitado por %s",
				peerID.String(), discoveryLegacyPeerTTL))
		}
	}
	return libp2pPeersResponse{}, err
}

// libp2pRequestAccess abre um stream /artifact/access/1.0.0 e retorna o token de acesso.
func libp2pRequestAccess(ctx context.Context, h host.Host, peerID peer.ID, artifactName, requesterID string) (P2PArtifactAccess, error) {
	s, err := h.NewStream(ctx, peerID, protoArtifactAccess)
	if err != nil {
		return P2PArtifactAccess{}, fmt.Errorf("stream access: %w", err)
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(libp2pStreamTimeout))

	req := libp2pAccessRequest{ArtifactName: artifactName, RequesterID: requesterID}
	if err := json.NewEncoder(s).Encode(req); err != nil {
		return P2PArtifactAccess{}, fmt.Errorf("encode access req: %w", err)
	}
	// Lê resposta: pode ser P2PArtifactAccess ou libp2pErrorResponse.
	raw, err := io.ReadAll(bufio.NewReader(s))
	if err != nil {
		return P2PArtifactAccess{}, fmt.Errorf("read access resp: %w", err)
	}
	var errResp libp2pErrorResponse
	if json.Unmarshal(raw, &errResp) == nil && errResp.Error != "" {
		return P2PArtifactAccess{}, peerStreamError(errResp)
	}
	var access P2PArtifactAccess
	if err := json.Unmarshal(raw, &access); err != nil {
		return P2PArtifactAccess{}, fmt.Errorf("decode access resp: %w", err)
	}
	return access, nil
}

// libp2pRequestDiscoveryAccess abre um stream /artifact/access/1.0.0 pedindo o
// token de discovery (artifact "*") e devolve a resposta com o token.
func libp2pRequestDiscoveryAccess(ctx context.Context, h host.Host, peerID peer.ID, requesterID string) (libp2pDiscoveryAccess, error) {
	s, err := h.NewStream(ctx, peerID, protoArtifactAccess)
	if err != nil {
		return libp2pDiscoveryAccess{}, fmt.Errorf("stream access: %w", err)
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(libp2pStreamTimeout))

	req := libp2pAccessRequest{ArtifactName: p2pDiscoveryTokenArtifact, RequesterID: requesterID}
	if err := json.NewEncoder(s).Encode(req); err != nil {
		return libp2pDiscoveryAccess{}, fmt.Errorf("encode access req: %w", err)
	}
	raw, err := io.ReadAll(bufio.NewReader(s))
	if err != nil {
		return libp2pDiscoveryAccess{}, fmt.Errorf("read access resp: %w", err)
	}
	var errResp libp2pErrorResponse
	if json.Unmarshal(raw, &errResp) == nil && errResp.Error != "" {
		return libp2pDiscoveryAccess{}, peerStreamError(errResp)
	}
	var access libp2pDiscoveryAccess
	if err := json.Unmarshal(raw, &access); err != nil {
		return libp2pDiscoveryAccess{}, fmt.Errorf("decode access resp: %w", err)
	}
	return access, nil
}

// libp2pFetchManifest abre um stream /artifact/manifest/1.0.0 e retorna o manifest de chunks.
// accessToken é o token de /artifact/access/1.0.0 (centenas de chunks por
// download só abrem manifest uma vez, mas o manifesto também exige credencial no
// modo "require"). Vazio = peer legacy: o servidor decide (aceita no modo
// "legacy", recusa no "require").
func libp2pFetchManifest(ctx context.Context, h host.Host, peerID peer.ID, artifactName, requesterID, accessToken string) (P2PChunkManifest, error) {
	s, err := h.NewStream(ctx, peerID, protoArtifactManifest)
	if err != nil {
		return P2PChunkManifest{}, fmt.Errorf("stream manifest: %w", err)
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(libp2pManifestTimeout))

	req := libp2pManifestRequest{
		ArtifactName: artifactName,
		RequesterID:  requesterID,
		Token:        strings.TrimSpace(accessToken),
	}
	if err := json.NewEncoder(s).Encode(req); err != nil {
		return P2PChunkManifest{}, fmt.Errorf("encode manifest req: %w", err)
	}
	raw, err := io.ReadAll(bufio.NewReader(s))
	if err != nil {
		return P2PChunkManifest{}, fmt.Errorf("read manifest resp: %w", err)
	}
	var errResp libp2pErrorResponse
	if json.Unmarshal(raw, &errResp) == nil && errResp.Error != "" {
		return P2PChunkManifest{}, peerStreamError(errResp)
	}
	var manifest P2PChunkManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return P2PChunkManifest{}, fmt.Errorf("decode manifest resp: %w", err)
	}
	return manifest, nil
}

func decodeLibp2pGetHeaderAndPayload(stream io.Reader) (libp2pGetResponse, io.Reader, error) {
	// O header é enviado via json.Encoder.Encode, que grava `{...}\n` (uma
	// única linha JSON seguida de newline). Lemos a linha inteira até o
	// primeiro '\n', que é o delimitador do Encoder. Todo o resto do stream é
	// o payload binário.
	//
	// IMPORTANTE: abordagens anteriores usavam json.Decoder + Peek/ReadByte
	// para remover o '\n' do header, o que era frágil: o payload binário pode
	// começar com 0x0A e o decoder pode ter consumido (ou não) o delimitador,
	// fazendo um byte legítimo do payload ser removido (checksum divergente).
	// Ler a linha do header e tratar o restante como payload puro é robusto.
	br := bufio.NewReader(stream)
	headerLine, err := br.ReadBytes('\n')
	if err != nil {
		return libp2pGetResponse{}, nil, fmt.Errorf("decode get hdr: %w", err)
	}
	var hdr libp2pGetResponse
	if err := json.Unmarshal(headerLine, &hdr); err != nil {
		return libp2pGetResponse{}, nil, fmt.Errorf("decode get hdr: %w", err)
	}
	if strings.TrimSpace(hdr.ArtifactName) == "" {
		return libp2pGetResponse{}, nil, fmt.Errorf("resposta de get invalida")
	}
	if hdr.RangeEnd < hdr.RangeStart {
		return libp2pGetResponse{}, nil, fmt.Errorf("range retornado inválido")
	}
	if payloadLen := hdr.RangeEnd - hdr.RangeStart + 1; payloadLen <= 0 {
		return libp2pGetResponse{}, nil, fmt.Errorf("payload vazio")
	}

	// Retorna um reader limitado ao tamanho EXATO do payload (conhecido no
	// header). Assim o caller lê exatamente payloadLen bytes do payload binário,
	// sem depender de nenhum delimitador entre header e payload.
	payloadLen := hdr.RangeEnd - hdr.RangeStart + 1
	payloadReader := io.LimitReader(br, payloadLen)
	return hdr, payloadReader, nil
}

func readPayloadExact(reader io.Reader, expected int64) ([]byte, error) {
	if expected < 0 {
		return nil, fmt.Errorf("tamanho de payload inválido")
	}
	if expected > int64(^uint(0)>>1) {
		return nil, fmt.Errorf("payload grande demais")
	}
	data := make([]byte, int(expected))
	n, err := io.ReadFull(reader, data)
	if err != nil {
		return nil, fmt.Errorf("leitura incompleta: esperado=%d recebido=%d: %w", expected, n, err)
	}
	return data, nil
}

// newChunkPayloadReader monta o pipeline de leitura de um chunk:
//
//	payload do stream                        (rede)
//	  → rollingDeadlineStreamReader          (renova o deadline a cada 512KB)
//	  → progressReader                       (callback de progresso, se houver)
//	  → bandwidthThrottleReader              (M15, se o limite estiver ativo)
//
// T4: o deadline rolante precisa envolver o leitor que REALMENTE é lido. Antes
// o progressReader era montado por cima do payload puro e o rolling era
// descartado — com callback de progresso o deadline nunca era renovado e
// streams lentos morriam no piso de 2min ("Application error 0x0" simultâneo).
func newChunkPayloadReader(ctx context.Context, payload io.Reader, stream p2pDeadlineSetter, chunkLen int64, onProgress func(readSoFar, total int64)) io.Reader {
	var dataReader io.Reader = &rollingDeadlineStreamReader{
		r:              payload,
		deadlineSetter: stream,
		interval:       rollingDeadlineInterval,
	}
	if onProgress != nil {
		dataReader = newProgressReader(dataReader, chunkLen, func(readSoFar int64) {
			onProgress(readSoFar, chunkLen)
		})
	}
	// M15: throttle de bandwidth (bucket global da máquina, configurável via
	// servidor) — consome tokens pelos bytes efetivamente baixados.
	if bwMaxPerSec.Load() > 0 {
		dataReader = newBandwidthThrottleReader(dataReader, ctx)
	}
	return dataReader
}

// libp2pDownloadChunk abre um stream /artifact/get/1.0.0, solicita um range e
// salva os bytes no destFile. Verifica SHA256 do chunk após receber.
// onProgress é opcional —" quando != nil, chamado com (bytesLidosNoChunk, tamanhoDoChunk).
// accessToken é o token emitido por /artifact/access/1.0.0 para (artifact, requesterID).
// Vazio = peer legado/negociação falhou: servidores em modo "require" recusam.
func libp2pDownloadChunk(ctx context.Context, h host.Host, peerID peer.ID, artifactName, requesterID, accessToken string, chunk P2PChunk, destFile string, onProgress func(readSoFar, total int64)) error {
	if chunk.Size <= 0 {
		return fmt.Errorf("chunk %d: tamanho inválido", chunk.Index)
	}
	if chunk.Offset < 0 {
		return fmt.Errorf("chunk %d: offset invalido", chunk.Index)
	}

	s, err := h.NewStream(ctx, peerID, protoArtifactGet)
	if err != nil {
		return fmt.Errorf("stream get: %w", err)
	}
	defer s.Close()
	_ = s.SetDeadline(computeTransferDeadline(chunk.Size))

	req := libp2pGetRequest{
		ArtifactName: artifactName,
		RequesterID:  requesterID,
		RangeStart:   chunk.Offset,
		RangeEnd:     chunk.Offset + chunk.Size - 1,
		Token:        accessToken,
	}
	if err := json.NewEncoder(s).Encode(req); err != nil {
		return fmt.Errorf("encode get req: %w", err)
	}

	hdr, payloadReader, err := decodeLibp2pGetHeaderAndPayload(s)
	if err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(hdr.ArtifactName), strings.TrimSpace(artifactName)) {
		return fmt.Errorf("chunk %d: artifact inesperado no payload", chunk.Index)
	}
	expectedStart := chunk.Offset
	expectedEnd := chunk.Offset + chunk.Size - 1
	if hdr.RangeStart != expectedStart || hdr.RangeEnd != expectedEnd {
		return fmt.Errorf("chunk %d: range inesperado no payload (esperado=%d-%d recebido=%d-%d)",
			chunk.Index, expectedStart, expectedEnd, hdr.RangeStart, hdr.RangeEnd)
	}

	chunkLen := hdr.RangeEnd - hdr.RangeStart + 1
	if chunkLen != chunk.Size {
		return fmt.Errorf("chunk %d: tamanho divergente (esperado=%d recebido=%d)", chunk.Index, chunk.Size, chunkLen)
	}
	// Pipeline de leitura do payload do chunk (ver newChunkPayloadReader).
	dataReader := newChunkPayloadReader(ctx, payloadReader, s, chunkLen, onProgress)

	// Streaming para disco: grava o chunk em um arquivo temporário enquanto
	// calcula o SHA256 em fluxo, evitando carregar o chunk inteiro em memória
	// (relevante com paralelismo alto e chunks de 8 MB).
	tmpFile := destFile + ".tmp"
	f, err := os.Create(tmpFile)
	if err != nil {
		return fmt.Errorf("criar arquivo temporario do chunk: %w", err)
	}
	hasher := sha256.New()
	written, copyErr := io.CopyN(io.MultiWriter(f, hasher), dataReader, chunkLen)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("leitura do chunk: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("fechar arquivo temporario do chunk: %w", closeErr)
	}
	if written != chunkLen {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("chunk %d: leitura incompleta (esperado=%d recebido=%d)", chunk.Index, chunkLen, written)
	}

	// Verificar hash do chunk.
	// hasher.Sum(nil) já é o SHA256 do chunk — verifySHA256Hex compara o digest
	// diretamente (a versão anterior re-hasheava o digest, causando falha em
	// 100% dos chunks mesmo com dados íntegros).
	if !verifySHA256Hex(hasher.Sum(nil), chunk.SHA256) {
		_ = os.Remove(tmpFile)
		// Diagnóstico: loga os prefixos dos hashes para identificar manifest
		// stale (cliente valida contra hashes de versão antiga do arquivo).
		gotShort := hex.EncodeToString(hasher.Sum(nil))
		if len(gotShort) > 12 {
			gotShort = gotShort[:12]
		}
		wantShort := chunk.SHA256
		if len(wantShort) > 12 {
			wantShort = wantShort[:12]
		}
		return fmt.Errorf("chunk %d: checksum divergente (esperado=%s obtido=%s): %w", chunk.Index, wantShort, gotShort, errChunkChecksumMismatch)
	}

	if err := os.Rename(tmpFile, destFile); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("chunk %d: renomear arquivo: %w", chunk.Index, err)
	}
	return nil
}

// libp2pDownloadArtifact faz download simples (arquivo inteiro) via /artifact/get/1.0.0.
// onProgress é opcional —" quando != nil, é chamado com (bytesLidos, totalBytes) durante a transferência.
func libp2pDownloadArtifact(ctx context.Context, h host.Host, peerID peer.ID, access P2PArtifactAccess, destDir string, onProgress func(readSoFar, total int64)) (string, int64, error) {
	s, err := h.NewStream(ctx, peerID, protoArtifactGet)
	if err != nil {
		return "", 0, fmt.Errorf("stream get: %w", err)
	}
	defer s.Close()
	// Deadline inicial: 2 min para handshake + header. Ser reajustado aps ler o header.
	_ = s.SetDeadline(time.Now().Add(libp2pTransferTimeout))

	req := libp2pGetRequest{
		ArtifactName: access.ArtifactName,
		RequesterID:  "peer-local",
		RangeStart:   0,
		RangeEnd:     -1,
		Token:        tokenFromAccessURL(access.URL),
	}
	if err := json.NewEncoder(s).Encode(req); err != nil {
		return "", 0, fmt.Errorf("encode get req: %w", err)
	}

	hdr, payloadReader, err := decodeLibp2pGetHeaderAndPayload(s)
	if err != nil {
		return "", 0, err
	}
	if !strings.EqualFold(strings.TrimSpace(hdr.ArtifactName), strings.TrimSpace(access.ArtifactName)) {
		return "", 0, fmt.Errorf("artifact inesperado no payload")
	}
	if hdr.RangeStart != 0 {
		return "", 0, fmt.Errorf("range inicial inesperado no payload")
	}
	expectedBytes := hdr.RangeEnd - hdr.RangeStart + 1

	// Reajustar deadline com base no tamanho real do payload.
	_ = s.SetDeadline(computeTransferDeadline(expectedBytes))

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", 0, err
	}
	targetPath := filepath.Join(destDir, access.ArtifactName)
	tmpPath := targetPath + ".partial"
	f, err := os.Create(tmpPath)
	if err != nil {
		return "", 0, err
	}

	var reader io.Reader = payloadReader
	if onProgress != nil {
		reader = newProgressReader(payloadReader, expectedBytes, func(readSoFar int64) {
			onProgress(readSoFar, expectedBytes)
		})
	}
	size, copyErr := io.CopyN(f, reader, expectedBytes)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return "", 0, fmt.Errorf("leitura incompleta do artifact: esperado=%d recebido=%d: %w", expectedBytes, size, copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return "", 0, closeErr
	}
	if size != expectedBytes {
		_ = os.Remove(tmpPath)
		return "", 0, fmt.Errorf("tamanho recebido divergente: esperado=%d recebido=%d", expectedBytes, size)
	}

	checksum, err := computeFileSHA256(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return "", 0, err
	}
	if strings.TrimSpace(access.ChecksumSHA256) != "" && !strings.EqualFold(strings.TrimSpace(access.ChecksumSHA256), checksum) {
		_ = os.Remove(tmpPath)
		return "", 0, fmt.Errorf("checksum divergente")
	}
	if err := platform.RenameAtomic(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", 0, err
	}
	_ = platform.EnsureWorldReadable(targetPath)
	return targetPath, size, nil
}
