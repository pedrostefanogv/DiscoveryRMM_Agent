// Package sessioncontrol define o contrato unico de controle para canais de
// sessao interativos (debug remoto, acesso remoto: tela/terminal/arquivos/
// processos/proxy).
//
// O MESMO subject carrega liveness (ping/pong) e comandos do dominio; a
// allow-list de tipos e FECHADA e definida pelo chamador (TypeSet), para que
// cada dominio execute apenas o que conhece. Como o subject tem pub+sub nos
// dois sentidos, cada lado descarta frames com o proprio "from" (o eco e
// esperado).
//
// O codec era exclusivo do remote debug; foi extraido para ser compartilhado
// sem duplicar a validacao (tamanho, tipo, papel, sessionId).
package sessioncontrol

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Papeis que podem emitir mensagens pelo canal de controle.
const (
	RoleViewer = "viewer"
	RoleAgent  = "agent"
	RoleServer = "server"
)

// MaxEnvelopeBytes limita o tamanho de um frame de controle.
const MaxEnvelopeBytes = 512

// Envelope e o contrato unico de controle entre navegador (viewer), servidor e
// agente.
type Envelope struct {
	Version      int            `json:"v"`
	Type         string         `json:"type"`
	SessionID    string         `json:"sessionId,omitempty"`
	From         string         `json:"from,omitempty"`
	Sequence     uint64         `json:"sequence,omitempty"`
	TimestampUTC string         `json:"timestampUtc,omitempty"`
	Payload      map[string]any `json:"payload,omitempty"`
}

// TypeSet e a allow-list fechada de tipos de um dominio.
type TypeSet map[string]struct{}

// NewTypeSet cria uma allow-list a partir dos tipos informados.
func NewTypeSet(types ...string) TypeSet {
	set := make(TypeSet, len(types))
	for _, t := range types {
		set[t] = struct{}{}
	}
	return set
}

// Has informa se o tipo pertence a allow-list.
func (s TypeSet) Has(typ string) bool {
	if s == nil {
		return false
	}
	_, ok := s[typ]
	return ok
}

// Tipos do remote debug (canal .remote-debug.control).
var RemoteDebugTypes = NewTypeSet("ping", "pong", "setLevel", "levelChanged", "closed")

// Tipos do acesso remoto (canal .remote-session.<id>.control).
var RemoteSessionTypes = NewTypeSet("ping", "pong", "keyframe", "closed")

// IsRole valida o campo from contra os papeis conhecidos.
func IsRole(role string) bool {
	switch role {
	case RoleViewer, RoleAgent, RoleServer:
		return true
	default:
		return false
	}
}

// NewEnvelope cria um envelope com versao, timestamp e papel fixados.
func NewEnvelope(from, typ, sessionID string, seq uint64, payload map[string]any) Envelope {
	return Envelope{
		Version:      1,
		Type:         typ,
		SessionID:    sessionID,
		From:         from,
		Sequence:     seq,
		TimestampUTC: time.Now().UTC().Format(time.RFC3339Nano),
		Payload:      payload,
	}
}

// Encode serializa o envelope validando tipo (allow-list) e tamanho.
func Encode(env Envelope, allowed TypeSet) ([]byte, error) {
	if env.Version == 0 {
		env.Version = 1
	}
	if !allowed.Has(env.Type) {
		return nil, fmt.Errorf("tipo de controle invalido: %q", env.Type)
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	if len(payload) > MaxEnvelopeBytes {
		return nil, fmt.Errorf("envelope de controle excede %d bytes (%d)", MaxEnvelopeBytes, len(payload))
	}
	return payload, nil
}

// Decode desserializa e valida um frame de controle recebido.
// Erros: payload vazio/grande, JSON invalido, tipo fora da allow-list, from
// desconhecido ou sessionId ausente.
func Decode(raw []byte, allowed TypeSet) (Envelope, error) {
	if len(raw) == 0 {
		return Envelope{}, fmt.Errorf("payload de controle vazio")
	}
	if len(raw) > MaxEnvelopeBytes {
		return Envelope{}, fmt.Errorf("envelope de controle excede %d bytes (%d)", MaxEnvelopeBytes, len(raw))
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return Envelope{}, fmt.Errorf("JSON de controle invalido: %w", err)
	}
	if !allowed.Has(env.Type) {
		return Envelope{}, fmt.Errorf("tipo de controle invalido: %q", env.Type)
	}
	if env.From == "" || !IsRole(env.From) {
		return Envelope{}, fmt.Errorf("from invalido: %q", env.From)
	}
	env.SessionID = strings.TrimSpace(env.SessionID)
	if env.SessionID == "" {
		return Envelope{}, fmt.Errorf("sessionId ausente no envelope de controle")
	}
	env.Version = 1
	return env, nil
}

// IsControlSubject verifica se o subject termina no sufixo canonico informado.
func IsControlSubject(subject, suffix string) bool {
	return strings.HasSuffix(strings.TrimSpace(subject), suffix)
}
