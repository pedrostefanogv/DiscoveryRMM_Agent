//go:build windows

package remotesession

import (
	"encoding/json"
	"strings"

	"discovery/app/core/sessioncontrol"
)

// Tipos de controle aceitos no canal .control da sessao remota. O mesmo canal
// carrega liveness (ping/pong), keyframe de tela e o aviso de encerramento.
const (
	ControlTypePing     = "ping"
	ControlTypePong     = "pong"
	ControlTypeKeyframe = "keyframe"
	ControlTypeClosed   = "closed"
	// ControlTypeInputLock é o comando do viewer para bloquear/destravar a
	// entrada (teclado/mouse) da máquina remota. Payload:
	// {locked, leaseSeconds?, maxSeconds?, query?}.
	ControlTypeInputLock = "inputLock"
	// ControlTypeInputLockChanged é o estado autoritativo publicado pelo agent
	// (também em resposta a query e em toda liberação automática).
	ControlTypeInputLockChanged = "inputLockChanged"
	// ControlTypeTransportInfo é o estado do transporte (nats | webrtc |
	// webrtc-fallback) publicado pelo agent no .control. Payload:
	// {"transport":{state,requested,active,reason,hasStun,hasTurn,relayOnly}}.
	//
	// O tipo PRECISA estar na allow-list do codec (sessioncontrol.
	// RemoteSessionTypes): sem isso o agente publicava no formato legado
	// {"action":"transport_info"}, que o decoder do console rejeita — ele exige o
	// campo "type". Era essa incompatibilidade que prendia o selo em "NATS"
	// mesmo com o peer connection conectado.
	ControlTypeTransportInfo = "transport_info"
	// ControlTypeWebRtcReady é o handshake de prontidão do viewer: ele avisa que
	// já assinou .webrtc.out e criou o peer, e o agent responde reenviando o
	// transport_info corrente. É idempotente e existe para fechar a corrida de
	// "o agent publica o estado antes de o viewer estar escutando".
	ControlTypeWebRtcReady = "webrtcReady"
)

// DecodeRemoteSessionControl decodifica um frame recebido no .control.
//
// Aceita o envelope tipado (ping/pong/keyframe/closed) e, para compatibilidade
// com viewers antigos de tela, o formato legado {"action":"keyframe"}. O
// envelope legado nao carrega sessionId: ele e aceito por chegar no subject
// literal da propria sessao.
func DecodeRemoteSessionControl(data []byte) (sessioncontrol.Envelope, error) {
	env, err := sessioncontrol.Decode(data, sessioncontrol.RemoteSessionTypes)
	if err == nil {
		return env, nil
	}

	// Limite vale tambem para o formato legado — sem isto um payload grande
	// cairia no fallback e driblaria o codec.
	if len(data) > sessioncontrol.MaxEnvelopeBytes {
		return sessioncontrol.Envelope{}, err
	}

	var legacy struct {
		Action string `json:"action"`
	}
	if jerr := json.Unmarshal(data, &legacy); jerr == nil &&
		strings.EqualFold(strings.TrimSpace(legacy.Action), ControlTypeKeyframe) {
		return sessioncontrol.NewEnvelope(sessioncontrol.RoleViewer, ControlTypeKeyframe, "", 0, nil), nil
	}
	return sessioncontrol.Envelope{}, err
}

// encodeRemoteSessionControl serializa um frame emitido pelo agente, validando
// o tipo contra a allow-list do dominio.
func encodeRemoteSessionControl(env sessioncontrol.Envelope) ([]byte, error) {
	return sessioncontrol.Encode(env, sessioncontrol.RemoteSessionTypes)
}
