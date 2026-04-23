package agentconn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

func parseHandshakeAck(raw any) (bool, string) {
	arr, ok := raw.([]any)
	if !ok {
		return true, ""
	}
	if len(arr) == 0 {
		return true, ""
	}

	success := true
	message := ""

	if b, ok := arr[0].(bool); ok {
		success = b
	}
	if len(arr) >= 2 {
		message = strings.TrimSpace(toString(arr[1]))
	}

	if m, ok := arr[0].(map[string]any); ok {
		if b, ok := m["success"].(bool); ok {
			success = b
		}
		if strings.TrimSpace(message) == "" {
			message = strings.TrimSpace(toString(m["message"]))
		}
	}

	return success, message
}

func (r *Runtime) sendHandshake(conn *websocket.Conn) error {
	// SignalR JSON protocol handshake frame.
	return conn.WriteMessage(websocket.TextMessage, []byte("{\"protocol\":\"json\",\"version\":1}\x1e"))
}

func (r *Runtime) waitHandshakeAck(conn *websocket.Conn, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = handshakeTimeout
	}
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	defer func() {
		_ = conn.SetReadDeadline(time.Time{})
	}()
	_, message, err := conn.ReadMessage()
	if err != nil {
		return err
	}
	records := splitSignalRRecords(message)
	for _, rec := range records {
		if strings.TrimSpace(rec) == "{}" || strings.TrimSpace(rec) == "" {
			return nil
		}
		var hs map[string]any
		if json.Unmarshal([]byte(rec), &hs) == nil {
			if e, ok := hs["error"].(string); ok && strings.TrimSpace(e) != "" {
				return fmt.Errorf("handshake rejeitado: %s", e)
			}
		}
	}
	return nil
}

func (r *Runtime) invoke(conn *websocket.Conn, target string, args ...any) error {
	frame := map[string]any{
		"type":      1,
		"target":    target,
		"arguments": args,
	}
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	payload = append(payload, 0x1e)
	return conn.WriteMessage(websocket.TextMessage, payload)
}

func executeCommand(parent context.Context, cmdType string, payload any) (int, string, string) {
	timeout := 2 * time.Minute
	command, args, pTimeout := parsePayload(payload)
	if pTimeout > 0 {
		timeout = pTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmdType = strings.ToLower(strings.TrimSpace(cmdType))
	var cmd *exec.Cmd

	switch cmdType {
	case "powershell", "ps":
		if command == "" {
			return 2, "", "payload sem comando powershell"
		}
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", command)
	case "cmd", "shell":
		if command == "" {
			return 2, "", "payload sem comando cmd/shell"
		}
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	case "exec", "process", "winget":
		if command == "" {
			return 2, "", "payload sem executavel"
		}
		// Resolve the executable to an absolute path before execution to prevent
		// PATH-hijacking and clarify to static analysis that the input is validated.
		resolved, err := exec.LookPath(command)
		if err != nil {
			// If not in PATH, accept only absolute or relative-with-extension paths.
			if !filepath.IsAbs(command) && filepath.Ext(command) == "" {
				return 2, "", fmt.Sprintf("executavel nao encontrado: %s", command)
			}
			resolved = command
		}
		cmd = exec.CommandContext(ctx, resolved, args...)
	default:
		if command == "" {
			return 2, "", "tipo de comando desconhecido e payload sem comando"
		}
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	}

	out, err := cmd.CombinedOutput()
	output := string(out)
	if err == nil {
		return 0, output, ""
	}

	exitCode := 1
	if exitErr, ok := err.(*exec.ExitError); ok {
		exitCode = exitErr.ExitCode()
	}
	errText := err.Error()
	if ctx.Err() == context.DeadlineExceeded {
		errText = "timeout excedido"
	}
	return exitCode, output, errText
}

func parsePayload(payload any) (string, []string, time.Duration) {
	if payload == nil {
		return "", nil, 0
	}

	if s, ok := payload.(string); ok {
		return strings.TrimSpace(s), nil, 0
	}

	m, ok := payload.(map[string]any)
	if !ok {
		return "", nil, 0
	}

	command := strings.TrimSpace(toString(m["command"]))
	if command == "" {
		command = strings.TrimSpace(toString(m["script"]))
	}
	args := toStringSlice(m["args"])
	timeoutSec, _ := toInt(m["timeoutSec"])
	if timeoutSec <= 0 {
		timeoutSec, _ = toInt(m["timeoutSeconds"])
	}
	if timeoutSec > 0 {
		return command, args, time.Duration(timeoutSec) * time.Second
	}
	return command, args, 0
}

func parseExecuteArgs(raw any) (cmdID, cmdType string, payload any) {
	arr, ok := raw.([]any)
	if ok {
		if len(arr) >= 3 {
			return toString(arr[0]), toString(arr[1]), arr[2]
		}
		if len(arr) == 1 {
			if m, ok := arr[0].(map[string]any); ok {
				return strings.TrimSpace(toString(m["cmdId"])), strings.TrimSpace(toString(m["cmdType"])), m["payload"]
			}
		}
	}
	return "", "", nil
}

func normalizeCommandType(raw any) string {
	return strings.ToLower(strings.TrimSpace(toString(raw)))
}

func parseSyncPingArgs(raw any) (SyncPing, bool) {
	arr, ok := raw.([]any)
	if !ok || len(arr) == 0 {
		return SyncPing{}, false
	}
	first, ok := arr[0].(map[string]any)
	if !ok {
		return SyncPing{}, false
	}

	ping := SyncPing{
		EventID:          strings.TrimSpace(toString(first["eventId"])),
		AgentID:          strings.TrimSpace(toString(first["agentId"])),
		EventType:        strings.TrimSpace(toString(first["eventType"])),
		Resource:         strings.TrimSpace(toString(first["resource"])),
		ScopeType:        strings.TrimSpace(toString(first["scopeType"])),
		ScopeID:          strings.TrimSpace(toString(first["scopeId"])),
		InstallationType: strings.TrimSpace(toString(first["installationType"])),
		Revision:         strings.TrimSpace(toString(first["revision"])),
		Reason:           strings.TrimSpace(toString(first["reason"])),
		ChangedAtUTC:     strings.TrimSpace(toString(first["changedAtUtc"])),
		CorrelationID:    strings.TrimSpace(toString(first["correlationId"])),
	}
	if ping.Resource == "" {
		return SyncPing{}, false
	}
	return ping, true
}

func splitSignalRRecords(data []byte) []string {
	parts := strings.Split(string(data), string([]byte{0x1e}))
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return strings.Trim(string(b), "\"")
	}
}

func toStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		s := strings.TrimSpace(toString(item))
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func toInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case int32:
		return int(t), true
	case int64:
		return int(t), true
	case string:
		if strings.TrimSpace(t) == "" {
			return 0, false
		}
		var i int
		_, err := fmt.Sscanf(t, "%d", &i)
		if err == nil {
			return i, true
		}
	}
	return 0, false
}

func detectLocalIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return addr.IP.String()
	}
	return "127.0.0.1"
}

func (r *Runtime) handleSignalRPayload(ctx context.Context, conn *websocket.Conn, payload []byte) error {
	for _, rec := range splitSignalRRecords(payload) {
		if strings.TrimSpace(rec) == "" || strings.TrimSpace(rec) == "{}" {
			continue
		}

		msg := map[string]any{}
		if err := json.Unmarshal([]byte(rec), &msg); err != nil {
			continue
		}

		t, _ := toInt(msg["type"])
		switch t {
		case 1:
			target, _ := msg["target"].(string)
			if strings.EqualFold(target, "ExecuteCommand") {
				cmdID, cmdType, cmdPayload := parseExecuteArgs(msg["arguments"])
				if cmdID == "" {
					r.logf("ExecuteCommand ignorado: cmdId vazio")
					continue
				}
				go r.executeAndRespond(ctx, conn, cmdID, cmdType, cmdPayload)
				continue
			}
			if strings.EqualFold(target, "SyncPing") {
				ping, ok := parseSyncPingArgs(msg["arguments"])
				if !ok {
					r.logf("SyncPing ignorado: payload invalido")
					continue
				}
				r.emitSyncPing(ping)
				continue
			}
		case 6:
			continue
		case 7:
			reason, _ := msg["error"].(string)
			if strings.TrimSpace(reason) == "" {
				reason = "servidor encerrou a conexao"
			}
			return errors.New(reason)
		}
	}
	return nil
}
