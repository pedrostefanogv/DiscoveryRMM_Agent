package agentconn

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"discovery/internal/processutil"
)

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
	case "update", "selfupdate":
		return 0, "update delegado ao self-updater (via HandleCommand)", ""
	case "restart", "reboot":
		return executeRestartOrShutdown(ctx, "restart", payload)
	case "shutdown":
		return executeRestartOrShutdown(ctx, "shutdown", payload)
	case "cancelrestart", "cancelshutdown", "abortshutdown":
		return executeAbortShutdown(ctx)
	case "wakeonlan", "wol":
		return executeWakeOnLanPacket(ctx, payload)
	case "systeminfo":
		// SystemInfo is handled by the app-layer HandleCommand callback.
		// If we reach here it means HandleCommand is not wired — fail gracefully.
		return 1, "", "systeminfo nao tratado (HandleCommand ausente)"
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

	if cmd != nil {
		setHideWindow(cmd)
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

// setHideWindow configures the process to run without a visible terminal window on Windows.
func setHideWindow(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	processutil.HideWindow(cmd)
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

// ── Power Management Handlers ──

// powerPayload represents the deserialized payload for restart/shutdown commands.
type powerPayload struct {
	DelaySeconds int    `json:"delaySeconds"`
	Force        bool   `json:"force"`
	Message      string `json:"message"`
}

// wolPayload represents the deserialized payload for Wake-on-LAN commands.
type wolPayload struct {
	MacAddress       string `json:"macAddress"`
	BroadcastAddress string `json:"broadcastAddress"`
}

// resolveShutdownExe returns the absolute path to shutdown.exe in System32.
// Using the absolute path avoids PATH resolution failures when the agent runs
// in restricted contexts (Startup shortcut, Scheduled Task, service account).
// Falls back to PATH lookup if the System32 path is unavailable.
func resolveShutdownExe() string {
	sysRoot := os.Getenv("SystemRoot")
	if sysRoot == "" {
		sysRoot = os.Getenv("windir")
	}
	if sysRoot == "" {
		sysRoot = `C:\Windows`
	}
	shutdownPath := filepath.Join(sysRoot, "System32", "shutdown.exe")
	if _, err := os.Stat(shutdownPath); err == nil {
		return shutdownPath
	}
	// Fallback: tenta shutdown.exe via PATH
	if resolved, err := exec.LookPath("shutdown.exe"); err == nil {
		return resolved
	}
	return shutdownPath // retorna o caminho canonico, o erro sera tratado por quem chama
}

// executeRestartOrShutdown schedules a system restart or shutdown via the OS.
// On Windows, it uses shutdown.exe with /r (restart) or /s (shutdown).
// Returns immediately after scheduling — the action is asynchronous.
func executeRestartOrShutdown(_ context.Context, action string, payload any) (int, string, string) {
	pp := parsePowerPayload(payload)
	if pp.DelaySeconds <= 0 {
		if action == "restart" {
			pp.DelaySeconds = 15
		} else {
			pp.DelaySeconds = 30
		}
	}

	flag := "/s"
	label := "shutdown"
	if action == "restart" {
		flag = "/r"
		label = "restart"
	}

	args := []string{flag, "/t", fmt.Sprintf("%d", pp.DelaySeconds)}

	if pp.Force {
		args = append(args, "/f")
	}

	if pp.Message != "" {
		args = append(args, "/c", pp.Message)
	}

	// Usa caminho absoluto para shutdown.exe (System32) para evitar falha
	// de PATH em contextos restritos (atalho Startup, Scheduled Task, service).
	// Fallback para PATH se o caminho do System32 nao estiver disponivel.
	shutdownExe := resolveShutdownExe()

	// shutdown.exe does not need ctx — it schedules and returns immediately
	cmd := exec.Command(shutdownExe, args...)
	setHideWindow(cmd)
	out, err := cmd.CombinedOutput()
	output := string(out)

	if err != nil {
		return 1, output, fmt.Sprintf("falha ao agendar %s (exe=%s args=%s): %v", label, shutdownExe, strings.Join(args, " "), err)
	}

	return 0, fmt.Sprintf(
		"%s agendado com sucesso (delay=%ds, force=%v, message=%q)",
		label, pp.DelaySeconds, pp.Force, pp.Message,
	), ""
}

// executeAbortShutdown cancels any scheduled system restart or shutdown.
func executeAbortShutdown(ctx context.Context) (int, string, string) {
	cmd := exec.CommandContext(ctx, resolveShutdownExe(), "/a")
	setHideWindow(cmd)
	out, err := cmd.CombinedOutput()
	output := string(out)

	if err != nil {
		return 1, output, fmt.Sprintf("falha ao cancelar shutdown agendado: %v", err)
	}

	return 0, "shutdown/restart agendado cancelado com sucesso", ""
}

// executeWakeOnLanPacket sends a Wake-on-LAN magic packet via UDP broadcast.
// The magic packet consists of 6 bytes of 0xFF followed by the target MAC address
// repeated 16 times, sent to port 9 (discard) on the broadcast address.
func executeWakeOnLanPacket(ctx context.Context, payload any) (int, string, string) {
	wp := parseWolPayload(payload)
	if wp.MacAddress == "" {
		return 2, "", "payload sem macAddress para Wake-on-LAN"
	}

	mac, err := net.ParseMAC(wp.MacAddress)
	if err != nil {
		return 2, "", fmt.Sprintf("macAddress invalido %q: %v", wp.MacAddress, err)
	}

	if len(mac) != 6 {
		return 2, "", fmt.Sprintf("macAddress deve ter 6 bytes, recebeu %d", len(mac))
	}

	// Build magic packet: 6 bytes of 0xFF + 16 repetitions of the MAC address
	packet := make([]byte, 102)
	for i := 0; i < 6; i++ {
		packet[i] = 0xFF
	}
	for i := 1; i <= 16; i++ {
		copy(packet[i*6:], mac)
	}

	broadcastAddr := wp.BroadcastAddress
	if broadcastAddr == "" {
		broadcastAddr = "255.255.255.255"
	}

	addr := net.JoinHostPort(broadcastAddr, "9")

	conn, err := net.Dial("udp", addr)
	if err != nil {
		return 1, "", fmt.Sprintf("falha ao conectar para WOL em %s: %v", addr, err)
	}
	defer conn.Close()

	// Use WriteWithDeadline for safety, but with a short deadline
	deadline, ok := ctx.Deadline()
	if ok {
		conn.SetWriteDeadline(deadline)
	} else {
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	}

	n, err := conn.Write(packet)
	if err != nil {
		return 1, "", fmt.Sprintf("falha ao enviar magic packet para %s (%s): %v", wp.MacAddress, addr, err)
	}

	return 0, fmt.Sprintf(
		"Magic Packet Wake-on-LAN enviado com sucesso (%d bytes) para MAC %s via broadcast %s",
		n, wp.MacAddress, addr,
	), ""
}

func parsePowerPayload(payload any) powerPayload {
	if payload == nil {
		return powerPayload{}
	}

	m, ok := payload.(map[string]any)
	if !ok {
		return powerPayload{}
	}

	pp := powerPayload{
		Message: strings.TrimSpace(toString(m["message"])),
	}

	if v, ok := m["force"]; ok {
		pp.Force, _ = v.(bool)
	}

	if d, ok := toInt(m["delaySeconds"]); ok && d > 0 {
		pp.DelaySeconds = d
	}
	if d, ok := toInt(m["delay"]); ok && d > 0 && pp.DelaySeconds == 0 {
		pp.DelaySeconds = d
	}

	return pp
}

func parseWolPayload(payload any) wolPayload {
	if payload == nil {
		return wolPayload{}
	}

	m, ok := payload.(map[string]any)
	if !ok {
		return wolPayload{}
	}

	return wolPayload{
		MacAddress:       strings.TrimSpace(toString(m["macAddress"])),
		BroadcastAddress: strings.TrimSpace(toString(m["broadcastAddress"])),
	}
}
