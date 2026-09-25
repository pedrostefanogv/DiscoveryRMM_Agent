package remotedebug

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Command representa um comando de remote debug enviado pelo servidor.
type Command struct {
	Action          string         `json:"action"`
	SessionID       string         `json:"sessionId"`
	LogLevel        string         `json:"logLevel"`
	StartedAtUTC    string         `json:"startedAtUtc"`
	ExpiresAtUTC    string         `json:"expiresAtUtc"`
	MaxExpiresAtUTC string         `json:"maxExpiresAtUtc"`
	StoppedAtUTC    string         `json:"stoppedAtUtc"`
	Liveness        LivenessConfig `json:"liveness"`
	Stream          StreamConfig   `json:"stream"`
}

// LivenessConfig parametriza o canal de controle da sessao. O servidor e a
// fonte dos valores; quando ausentes (servidor antigo/omitido), valem os
// defaults de NormalizeLiveness.
type LivenessConfig struct {
	PingIntervalSeconds    int `json:"pingIntervalSeconds"`
	MissedPingsBeforeClose int `json:"missedPingsBeforeClose"`
	InitialGraceSeconds    int `json:"initialGraceSeconds"`
	KeepAliveSeconds       int `json:"keepAliveSeconds"`
}

// commandPayload é o payload bruto tolerante a null vindo do servidor.
type commandPayload struct {
	Action          *string          `json:"action"`
	SessionID       *string          `json:"sessionId"`
	LogLevel        *string          `json:"logLevel"`
	StartedAtUTC    *string          `json:"startedAtUtc"`
	ExpiresAtUTC    *string          `json:"expiresAtUtc"`
	MaxExpiresAtUTC *string          `json:"maxExpiresAtUtc"`
	StoppedAtUTC    *string          `json:"stoppedAtUtc"`
	Liveness        *livenessPayload `json:"liveness"`
	Stream          *streamPayload   `json:"stream"`
}

// livenessPayload e o payload bruto tolerante a null do bloco liveness.
type livenessPayload struct {
	PingIntervalSeconds    *int `json:"pingIntervalSeconds"`
	MissedPingsBeforeClose *int `json:"missedPingsBeforeClose"`
	InitialGraceSeconds    *int `json:"initialGraceSeconds"`
	KeepAliveSeconds       *int `json:"keepAliveSeconds"`
}

// StreamConfig configura o transporte de stream de logs.
type StreamConfig struct {
	NatsSubject        string `json:"natsSubject"`
	NatsWssURL         string `json:"natsWssUrl"`
	NatsControlSubject string `json:"natsControlSubject"`
}

type streamPayload struct {
	NatsSubject        *string `json:"natsSubject"`
	NatsWssURL         *string `json:"natsWssUrl"`
	NatsControlSubject *string `json:"natsControlSubject"`
}

// LogMessage é a mensagem de log publicada no stream remoto.
type LogMessage struct {
	SessionID    string `json:"sessionId"`
	AgentID      string `json:"agentId"`
	Message      string `json:"message"`
	Level        string `json:"level"`
	TimestampUTC string `json:"timestampUtc"`
	Sequence     uint64 `json:"sequence"`
}

// IsCommandType verifica se cmdType corresponde a um comando de remote debug.
func IsCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "8", "remotedebug", "remote-debug":
		return true
	default:
		return false
	}
}

// ParseCommand converte um payload bruto em um Command normalizado.
func ParseCommand(payload any) (Command, error) {
	if payload == nil {
		return Command{}, fmt.Errorf("payload ausente")
	}
	b, err := decodePayloadBytes(payload)
	if err != nil {
		return Command{}, err
	}
	var raw commandPayload
	if err := json.Unmarshal(b, &raw); err != nil {
		return Command{}, err
	}
	cmd := Command{
		Action:          strings.TrimSpace(ptrStringOrEmpty(raw.Action)),
		SessionID:       strings.TrimSpace(ptrStringOrEmpty(raw.SessionID)),
		LogLevel:        strings.TrimSpace(ptrStringOrEmpty(raw.LogLevel)),
		StartedAtUTC:    strings.TrimSpace(ptrStringOrEmpty(raw.StartedAtUTC)),
		ExpiresAtUTC:    strings.TrimSpace(ptrStringOrEmpty(raw.ExpiresAtUTC)),
		MaxExpiresAtUTC: strings.TrimSpace(ptrStringOrEmpty(raw.MaxExpiresAtUTC)),
		StoppedAtUTC:    strings.TrimSpace(ptrStringOrEmpty(raw.StoppedAtUTC)),
	}
	if raw.Stream != nil {
		cmd.Stream.NatsSubject = strings.TrimSpace(ptrStringOrEmpty(raw.Stream.NatsSubject))
		cmd.Stream.NatsWssURL = strings.TrimSpace(ptrStringOrEmpty(raw.Stream.NatsWssURL))
		cmd.Stream.NatsControlSubject = strings.TrimSpace(ptrStringOrEmpty(raw.Stream.NatsControlSubject))
	}
	cmd.Liveness = NormalizeLiveness(raw.Liveness)
	cmd.LogLevel = NormalizeLevel(cmd.LogLevel)
	return cmd, nil
}

// NormalizeLiveness aplica defaults seguros quando o servidor nao envia o
// bloco de liveness (ou envia valores invalidos).
func NormalizeLiveness(raw *livenessPayload) LivenessConfig {
	cfg := LivenessConfig{
		PingIntervalSeconds:    DefaultPingIntervalSeconds,
		MissedPingsBeforeClose: DefaultMissedPingsBeforeClose,
		InitialGraceSeconds:    DefaultInitialGraceSeconds,
		KeepAliveSeconds:       DefaultKeepAliveSeconds,
	}
	if raw == nil {
		return cfg
	}
	if v := ptrIntOrZero(raw.PingIntervalSeconds); v > 0 {
		cfg.PingIntervalSeconds = v
	}
	if v := ptrIntOrZero(raw.MissedPingsBeforeClose); v > 0 {
		cfg.MissedPingsBeforeClose = v
	}
	if v := ptrIntOrZero(raw.InitialGraceSeconds); v > 0 {
		cfg.InitialGraceSeconds = v
	}
	if v := ptrIntOrZero(raw.KeepAliveSeconds); v > 0 {
		cfg.KeepAliveSeconds = v
	}
	return cfg
}

func ptrIntOrZero(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func decodePayloadBytes(payload any) ([]byte, error) {
	switch typed := payload.(type) {
	case string:
		raw := strings.TrimSpace(typed)
		if raw == "" {
			return nil, fmt.Errorf("payload ausente")
		}
		return []byte(raw), nil
	case []byte:
		raw := bytes.TrimSpace(typed)
		if len(raw) == 0 {
			return nil, fmt.Errorf("payload ausente")
		}
		return raw, nil
	case json.RawMessage:
		raw := bytes.TrimSpace(typed)
		if len(raw) == 0 {
			return nil, fmt.Errorf("payload ausente")
		}
		return raw, nil
	default:
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		raw := bytes.TrimSpace(b)
		if len(raw) == 0 || strings.EqualFold(string(raw), "null") {
			return nil, fmt.Errorf("payload ausente")
		}
		return raw, nil
	}
}

func ptrStringOrEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
