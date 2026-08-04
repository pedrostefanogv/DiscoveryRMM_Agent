package remotedebug

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Command representa um comando de remote debug enviado pelo servidor.
type Command struct {
	Action       string       `json:"action"`
	SessionID    string       `json:"sessionId"`
	LogLevel     string       `json:"logLevel"`
	StartedAtUTC string       `json:"startedAtUtc"`
	ExpiresAtUTC string       `json:"expiresAtUtc"`
	StoppedAtUTC string       `json:"stoppedAtUtc"`
	Stream       StreamConfig `json:"stream"`
}

// commandPayload é o payload bruto tolerante a null vindo do servidor.
type commandPayload struct {
	Action       *string        `json:"action"`
	SessionID    *string        `json:"sessionId"`
	LogLevel     *string        `json:"logLevel"`
	StartedAtUTC *string        `json:"startedAtUtc"`
	ExpiresAtUTC *string        `json:"expiresAtUtc"`
	StoppedAtUTC *string        `json:"stoppedAtUtc"`
	Stream       *streamPayload `json:"stream"`
}

// StreamConfig configura o transporte de stream de logs.
type StreamConfig struct {
	NatsSubject string `json:"natsSubject"`
	NatsWssURL  string `json:"natsWssUrl"`
}

type streamPayload struct {
	NatsSubject *string `json:"natsSubject"`
	NatsWssURL  *string `json:"natsWssUrl"`
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
		Action:       strings.TrimSpace(ptrStringOrEmpty(raw.Action)),
		SessionID:    strings.TrimSpace(ptrStringOrEmpty(raw.SessionID)),
		LogLevel:     strings.TrimSpace(ptrStringOrEmpty(raw.LogLevel)),
		StartedAtUTC: strings.TrimSpace(ptrStringOrEmpty(raw.StartedAtUTC)),
		ExpiresAtUTC: strings.TrimSpace(ptrStringOrEmpty(raw.ExpiresAtUTC)),
		StoppedAtUTC: strings.TrimSpace(ptrStringOrEmpty(raw.StoppedAtUTC)),
	}
	if raw.Stream != nil {
		cmd.Stream.NatsSubject = strings.TrimSpace(ptrStringOrEmpty(raw.Stream.NatsSubject))
		cmd.Stream.NatsWssURL = strings.TrimSpace(ptrStringOrEmpty(raw.Stream.NatsWssURL))
	}
	cmd.LogLevel = NormalizeLevel(cmd.LogLevel)
	return cmd, nil
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
