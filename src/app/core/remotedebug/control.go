package remotedebug

import (
	"fmt"
	"strings"

	"discovery/app/core/sessioncontrol"
)

// Papeis que podem emitir mensagens pelo canal de controle do remote debug.
const (
	RoleViewer = sessioncontrol.RoleViewer
	RoleAgent  = sessioncontrol.RoleAgent
	RoleServer = sessioncontrol.RoleServer
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
const MaxControlEnvelopeBytes = sessioncontrol.MaxEnvelopeBytes

// ControlSubjectSuffix e o sufixo canonico do canal de controle.
const ControlSubjectSuffix = "remote-debug.control"

// IsControlSubject verifica se o subject termina no sufixo canonico.
func IsControlSubject(subject string) bool {
	return sessioncontrol.IsControlSubject(subject, ControlSubjectSuffix)
}

// ControlEnvelope e o contrato unico de controle entre navegador (viewer),
// servidor e agente. O MESMO subject carrega liveness (ping/pong) e comandos
// (setLevel), evitando criar um subject/ACL novo a cada mensagem futura.
// Como o subject tem pub+sub nos dois sentidos, cada lado descarta frames
// com o proprio from (o eco e esperado).
//
// Alias do codec compartilhado em sessioncontrol (mesmo contrato usado pelo
// acesso remoto).
type ControlEnvelope = sessioncontrol.Envelope

// NewControlEnvelope cria um envelope com versao, timestamp e papel fixados.
func NewControlEnvelope(from, typ, sessionID string, seq uint64, payload map[string]any) ControlEnvelope {
	return sessioncontrol.NewEnvelope(from, typ, sessionID, seq, payload)
}

// EncodeControl serializa o envelope validando tipo e tamanho.
func EncodeControl(env ControlEnvelope) ([]byte, error) {
	return sessioncontrol.Encode(env, sessioncontrol.RemoteDebugTypes)
}

// DecodeControl desserializa e valida um frame de controle recebido.
// Erros: payload vazio/grande, JSON invalido, tipo fora da allow-list, from
// desconhecido ou sessionId ausente.
func DecodeControl(raw []byte) (ControlEnvelope, error) {
	return sessioncontrol.Decode(raw, sessioncontrol.RemoteDebugTypes)
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
