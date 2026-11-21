package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

type sessionToolResult struct {
	Result string
	Error  string
}

var (
	sessionToolCallsMu sync.Mutex
	sessionToolCalls   = map[string]chan sessionToolResult{}
)

// ExecuteMCPToolInUserSession encaminha a execução de uma tool de sessão do
// usuário do serviço (Session 0 / SYSTEM) para a UI companion conectada via IPC.
// Em modo standalone (a.ipcServer == nil) devolve handled=false para execução
// local normal.
func (a *App) ExecuteMCPToolInUserSession(ctx context.Context, toolName, argsJSON string) (string, bool, error) {
	if a == nil || a.ipcServer == nil {
		return "", false, nil
	}
	if a.ipcServer.ClientCount() == 0 {
		return "", true, fmt.Errorf("ação '%s' requer uma sessão interativa do usuário ativa (nenhuma UI companion conectada)", toolName)
	}

	callID := fmt.Sprintf("sess_%d", time.Now().UnixNano())
	ch := make(chan sessionToolResult, 1)

	sessionToolCallsMu.Lock()
	sessionToolCalls[callID] = ch
	sessionToolCallsMu.Unlock()

	defer func() {
		sessionToolCallsMu.Lock()
		delete(sessionToolCalls, callID)
		sessionToolCallsMu.Unlock()
	}()

	a.ipcServer.Broadcast(NewIPCMessage(IPCMsgEvent, map[string]any{
		"name":     "mcp:exec_user_session",
		"callId":   callID,
		"toolName": toolName,
		"argsJSON": argsJSON,
	}))

	select {
	case res := <-ch:
		if res.Error != "" {
			return "", true, fmt.Errorf("%s", res.Error)
		}
		return res.Result, true, nil
	case <-ctx.Done():
		return "", true, ctx.Err()
	}
}

// handleIPCSessionToolResult recebe o resultado da UI companion (lado serviço).
func (a *App) handleIPCSessionToolResult(parsed map[string]any) {
	if parsed == nil {
		return
	}
	callID, _ := parsed["callId"].(string)
	if callID == "" {
		return
	}
	resStr, _ := parsed["result"].(string)
	errStr, _ := parsed["error"].(string)

	sessionToolCallsMu.Lock()
	ch := sessionToolCalls[callID]
	sessionToolCallsMu.Unlock()
	if ch != nil {
		select {
		case ch <- sessionToolResult{Result: resStr, Error: errStr}:
		default:
		}
	}
}

// handleCompanionExecUserSessionTool executa uma tool de sessão do usuário no
// processo UI companion e devolve o resultado ao serviço via IPCMsgCommandResult.
func (a *App) handleCompanionExecUserSessionTool(payload map[string]any) {
	if a == nil || a.ipcClient == nil || a.mcpRegistry == nil {
		return
	}
	callID, _ := payload["callId"].(string)
	toolName, _ := payload["toolName"].(string)
	argsJSON, _ := payload["argsJSON"].(string)
	if callID == "" || toolName == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	result, err := a.mcpRegistry.Call(ctx, toolName, json.RawMessage(argsJSON))
	var resStr, errStr string
	if err != nil {
		errStr = err.Error()
	} else {
		if b, mErr := json.Marshal(result); mErr == nil {
			resStr = string(b)
		} else {
			resStr = fmt.Sprintf(`{"result":%q}`, fmt.Sprint(result))
		}
	}

	_ = a.ipcClient.Send(NewIPCMessage(IPCMsgCommandResult, map[string]any{
		"callId": callID,
		"result": resStr,
		"error":  errStr,
	}))
}
