package remotedebug

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Papeis que podem emitir mensagens pelo canal de controle do remote debug.
const (
	RoleViewer = "viewer"
	RoleAgent  = "agent"
	RoleServer = "server"
)

// Tipos de mensagem do canal de controle. A lista e FECHADA: o agente nunca
// executa nada fora dela (o canal nao vira um executor livre de comandos).
const (
	ControlTypePing         = "ping"
	ControlTypePong         = "pong"
	ControlTypeSetLevel     = "setLevel"
	ControlTypeLevelChanged = "levelChanged"
	ControlTypeClosed       = "closed"
)

// MaxControlEnvelopeBytes limita o tamanho de um frame de controle.
const MaxControlEnvelopeBytes = 512

// ControlSubjectSuffix e o sufixo canonico do canal de controle.
const ControlSubjectSuffix = "remote-debug.control"

// IsControlSubject verifica se o subject termina no sufixo canonico.
func IsControlSubject(subject string) bool {
	return strings.HasSuffix(strings.TrimSpace(subject), ControlSubjectSuffix)
}

// isControlType valida o tipo contra a allow-list fechada.
func isControlType(t string) bool {
	switch t {
	case ControlTypePing, ControlTypePong, ControlTypeSetLevel, ControlTypeLevelChanged, ControlTypeClosed:
		return true
	default:
		return false
	}
}

// isControlRole valida o campo from contra os papeis conhecidos.
func isControlRole(r string) bool {
	switch r {
	case RoleViewer, RoleAgent, RoleServer:
		return true
	default:
		return false
	}
}

// ControlEnvelope e o contrato unico de controle entre navegador (viewer),
// servidor e agente. O MESMO subject carrega liveness (ping/pong) e comandos
// (setLevel), evitando criar um subject/ACL novo a cada mensagem futura.
// Como o subject tem pub+sub nos dois sentidos, cada lado descarta frames
// com o proprio from (o eco e esperado).
type ControlEnvelope struct {
	Version      int            `json:"v"`
	Type         string         `json:"type"`
	SessionID    string         `json:"sessionId,omitempty"`
	From         string         `json:"from,omitempty"`
	Sequence     uint64         `json:"sequence,omitempty"`
	TimestampUTC string         `json:"timestampUtc,omitempty"`
	Payload      map[string]any `json:"payload,omitempty"`
}

// NewControlEnvelope cria um envelope com versao, timestamp e papel fixados.
func NewControlEnvelope(from, typ, sessionID string, seq uint64, payload map[string]any) ControlEnvelope {
	return ControlEnvelope{
		Version:      1,
		Type:         typ,
		SessionID:    sessionID,
		From:         from,
		Sequence:     seq,
		TimestampUTC: time.Now().UTC().Format(time.RFC3339Nano),
		Payload:      payload,
	}
}

// EncodeControl serializa o envelope validando tipo e tamanho.
func EncodeControl(env ControlEnvelope) ([]byte, error) {
	if env.Version == 0 {
		env.Version = 1
	}
	if !isControlType(env.Type) {
		return nil, fmt.Errorf("tipo de controle invalido: %q", env.Type)
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	if len(payload) > MaxControlEnvelopeBytes {
		return nil, fmt.Errorf("envelope de controle excede %d bytes (%d)", MaxControlEnvelopeBytes, len(payload))
	}
	return payload, nil
}

// DecodeControl desserializa e valida um frame de controle recebido.
// Erros: payload vazio/grande, JSON invalido, tipo fora da allow-list, from
// desconhecido ou sessionId ausente.
func DecodeControl(raw []byte) (ControlEnvelope, error) {
	if len(raw) == 0 {
		return ControlEnvelope{}, fmt.Errorf("payload de controle vazio")
	}
	if len(raw) > MaxControlEnvelopeBytes {
		return ControlEnvelope{}, fmt.Errorf("envelope de controle excede %d bytes (%d)", MaxControlEnvelopeBytes, len(raw))
	}
	var env ControlEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return ControlEnvelope{}, fmt.Errorf("JSON de controle invalido: %w", err)
	}
	if !isControlType(env.Type) {
		return ControlEnvelope{}, fmt.Errorf("tipo de controle invalido: %q", env.Type)
	}
	if env.From == "" || !isControlRole(env.From) {
		return ControlEnvelope{}, fmt.Errorf("from invalido: %q", env.From)
	}
	env.SessionID = strings.TrimSpace(env.SessionID)
	if env.SessionID == "" {
		return ControlEnvelope{}, fmt.Errorf("sessionId ausente no envelope de controle")
	}
	env.Version = 1
	return env, nil
}

// ResolveControlSubjectPrefixed resolve o subject de controle da sessao.
// Preferencia: campo explicito do comando (validado pelo sufixo canonico).
// Fallback: derivacao do subject de log resolvido com os placeholders.
func ResolveControlSubjectPrefixed(stream StreamConfig, clientID, siteID, agentID string) (string, error) {
	if explicit := strings.TrimSpace(stream.NatsControlSubject); explicit != "" {
		if !IsControlSubject(explicit) {
			return "", fmt.Errorf("subject de controle invalido: esperado sufixo %s, recebido=%q", ControlSubjectSuffix, explicit)
		}
		return explicit, nil
	}
	resolved := ResolveSubject(strings.TrimSpace(stream.NatsSubject), clientID, siteID, agentID)
	return ControlSubjectFromLogSubject(resolved)
}

// ControlSubjectFromLogSubject deriva o subject de controle a partir do
// subject canonico de log (…remote-debug.log -> …remote-debug.control).
// O servidor envia o subject explicito no comando; esta derivacao e o
// fallback quando o campo vem vazio.
func ControlSubjectFromLogSubject(logSubject string) (string, error) {
	logSubject = strings.TrimSpace(logSubject)
	if logSubject == "" {
		return "", fmt.Errorf("subject de remote debug ausente")
	}
	if !IsCanonicalSubject(logSubject) {
		return "", fmt.Errorf("subject nao canonico: %q", logSubject)
	}
	return logSubject[:len(logSubject)-len(".log")] + ".control", nil
}
