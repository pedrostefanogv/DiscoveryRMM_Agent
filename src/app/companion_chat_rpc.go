package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"discovery/app/services/chat"
	"discovery/app/supportchat"
)

// ── Chat IA (companion → serviço via IPC) ───────────────────────────────────

func (a *App) ipcRPCChatGetConfig() map[string]any {
	if a.chatSvc == nil {
		return map[string]any{"ok": false, "error": "chat service indisponível"}
	}
	raw, err := json.Marshal(a.chatSvc.GetConfig())
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true, "data": map[string]any{"config": json.RawMessage(raw)}}
}

func (a *App) ipcRPCChatSetConfig(payload map[string]any) map[string]any {
	if a.chatSvc == nil {
		return map[string]any{"ok": false, "error": "chat service indisponível"}
	}
	raw, err := json.Marshal(payload["config"])
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	var cfg chat.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	if err := a.chatSvc.SetConfig(cfg); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true}
}

func (a *App) ipcRPCChatTestConfig(payload map[string]any) map[string]any {
	if a.chatSvc == nil {
		return map[string]any{"ok": false, "error": "chat service indisponível"}
	}
	raw, err := json.Marshal(payload["config"])
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	var cfg chat.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	out, err := a.chatSvc.TestConfig(cfg)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true, "data": map[string]any{"output": out}}
}

func (a *App) ipcRPCChatSend(payload map[string]any) map[string]any {
	if a.chatSvc == nil {
		return map[string]any{"ok": false, "error": "chat service indisponível"}
	}
	msg, _ := payload["message"].(string)
	out, err := a.chatSvc.SendMessage(msg)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true, "data": map[string]any{"output": out}}
}

func (a *App) ipcRPCChatStream(payload map[string]any) map[string]any {
	if a.chatSvc == nil {
		return map[string]any{"ok": false, "error": "chat service indisponível"}
	}
	msg, _ := payload["message"].(string)
	a.chatSvc.StartStream(msg)
	return map[string]any{"ok": true}
}

func (a *App) ipcRPCChatStreamImages(payload map[string]any) map[string]any {
	if a.chatSvc == nil {
		return map[string]any{"ok": false, "error": "chat service indisponível"}
	}
	msg, _ := payload["message"].(string)
	imagesJSON, _ := payload["imagesJSON"].(string)
	a.chatSvc.StartStreamWithImages(msg, imagesJSON)
	return map[string]any{"ok": true}
}

func (a *App) ipcRPCChatStop() map[string]any {
	if a.chatSvc == nil {
		return map[string]any{"ok": true, "data": map[string]any{"stopped": false}}
	}
	return map[string]any{"ok": true, "data": map[string]any{"stopped": a.chatSvc.StopStream()}}
}

func (a *App) ipcRPCChatActive() map[string]any {
	if a.chatSvc == nil {
		return map[string]any{"ok": true, "data": map[string]any{"active": false}}
	}
	return map[string]any{"ok": true, "data": map[string]any{"active": a.chatSvc.HasActiveStream()}}
}

func (a *App) ipcRPCChatClear() map[string]any {
	if a.chatSvc != nil {
		a.chatSvc.ClearHistory()
	}
	return map[string]any{"ok": true}
}

func (a *App) ipcRPCChatHistory() map[string]any {
	if a.chatSvc == nil {
		return map[string]any{"ok": true, "data": map[string]any{"history": []ChatMessage{}}}
	}
	history := a.chatSvc.GetHistory()
	msgs := make([]ChatMessage, 0, len(history))
	for _, m := range history {
		msgs = append(msgs, ChatMessage{Role: m.Role, Content: m.Content})
	}
	raw, err := json.Marshal(msgs)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true, "data": map[string]any{"history": json.RawMessage(raw)}}
}

func (a *App) ipcRPCChatTools() map[string]any {
	if a.chatSvc == nil {
		return map[string]any{"ok": true, "data": map[string]any{"tools": []map[string]string{}}}
	}
	raw, err := json.Marshal(a.chatSvc.GetAvailableTools())
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true, "data": map[string]any{"tools": json.RawMessage(raw)}}
}

func (a *App) ipcRPCChatRegisterTools() map[string]any {
	if a.chatSvc == nil {
		return map[string]any{"ok": false, "error": "chat service indisponível"}
	}
	if err := a.chatSvc.RegisterToolsOnServer(); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true}
}

func (a *App) ipcRPCChatAnswerQuestion(payload map[string]any) map[string]any {
	questionID, _ := payload["questionID"].(string)
	answer, _ := payload["answer"].(string)
	a.AnswerChatQuestion(questionID, answer)
	return map[string]any{"ok": true}
}

func (a *App) ipcRPCChatAnswerA2uiAction(payload map[string]any) map[string]any {
	payloadJSON, _ := payload["payloadJSON"].(string)
	a.AnswerA2uiAction(payloadJSON)
	return map[string]any{"ok": true}
}

// ── Chat de Suporte (companion → serviço via IPC) ───────────────────────────

func (a *App) ipcRPCSupportChatRefresh(keepInvite bool) map[string]any {
	ctx, cancel := context.WithTimeout(a.supportChatContext(), supportChatCallWait)
	defer cancel()
	var (
		st  supportchat.State
		err error
	)
	if keepInvite {
		st, err = a.supportChatService().RefreshKeepingInvite(ctx)
	} else {
		st, err = a.supportChatService().Refresh(ctx)
	}
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true, "data": map[string]any{"state": json.RawMessage(raw)}}
}

func (a *App) ipcRPCSupportChatSend(payload map[string]any) map[string]any {
	body, _ := payload["body"].(string)
	var ids []string
	if rawIDs, ok := payload["attachmentIds"].([]any); ok {
		for _, v := range rawIDs {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				ids = append(ids, s)
			}
		}
	}
	ctx, cancel := context.WithTimeout(a.supportChatContext(), supportChatCallWait)
	defer cancel()
	if err := a.supportChatService().SendMessage(ctx, body, ids); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true}
}

func (a *App) ipcRPCSupportChatTyping(payload map[string]any) map[string]any {
	isTyping, _ := payload["isTyping"].(bool)
	ctx, cancel := context.WithTimeout(a.supportChatContext(), 5*time.Second)
	defer cancel()
	if err := a.supportChatService().SendTyping(ctx, isTyping); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true}
}

func (a *App) ipcRPCSupportChatUpload(payload map[string]any) map[string]any {
	fileName, _ := payload["fileName"].(string)
	contentType, _ := payload["contentType"].(string)
	b64, _ := payload["base64Data"].(string)
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return map[string]any{"ok": false, "error": errSupportChatInvalidAttachment.Error()}
	}
	ctx, cancel := context.WithTimeout(a.supportChatContext(), supportChatCallWait)
	defer cancel()
	id, err := a.supportChatService().UploadAttachment(ctx, fileName, contentType, data)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true, "data": map[string]any{"attachmentId": id}}
}

func (a *App) ipcRPCSupportChatDownload(payload map[string]any) map[string]any {
	attachmentID, _ := payload["attachmentId"].(string)
	ctx, cancel := context.WithTimeout(a.supportChatContext(), supportChatCallWait)
	defer cancel()
	data, contentType, err := a.supportChatService().DownloadAttachment(ctx, attachmentID)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return map[string]any{
		"ok": true,
		"data": map[string]any{
			"contentType": contentType,
			"base64":      base64.StdEncoding.EncodeToString(data),
		},
	}
}

func (a *App) ipcRPCSupportChatEnd(payload map[string]any) map[string]any {
	reason, _ := payload["reason"].(string)
	ctx, cancel := context.WithTimeout(a.supportChatContext(), supportChatCallWait)
	defer cancel()
	if err := a.supportChatService().End(ctx, reason); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true}
}

func (a *App) ipcRPCSupportChatRespondInvite(payload map[string]any) map[string]any {
	sessionID, _ := payload["sessionId"].(string)
	accepted, _ := payload["accepted"].(bool)
	reason, _ := payload["reason"].(string)
	ctx, cancel := context.WithTimeout(a.supportChatContext(), supportChatCallWait)
	defer cancel()
	var err error
	if accepted {
		err = a.supportChatService().RespondInviteAcceptedForSession(ctx, sessionID)
	} else {
		err = a.supportChatService().RespondInviteForSession(ctx, sessionID, false, reason)
	}
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true}
}

func (a *App) ipcRPCSupportChatActiveSession() map[string]any {
	ctx, cancel := context.WithTimeout(a.supportChatContext(), supportChatCallWait)
	defer cancel()
	id, has, err := a.supportChatService().ActiveSessionID(ctx)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true, "data": map[string]any{"sessionId": id, "hasSession": has}}
}

func (a *App) ipcRPCSupportChatPolicy() map[string]any {
	ctx, cancel := context.WithTimeout(a.supportChatContext(), supportChatCallWait)
	defer cancel()
	policy, err := a.supportChatService().FetchAttachmentPolicy(ctx)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true, "data": map[string]any{"policy": json.RawMessage(raw)}}
}

// ── Helpers lado UI (companion) ─────────────────────────────────────────────

func (a *App) ipcCallError(method string, payload map[string]any, timeout time.Duration) (map[string]any, error) {
	if a == nil || a.ipcClient == nil {
		return nil, fmt.Errorf("ipc indisponível")
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := a.ipcClient.Request(ctx, method, payload)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("sem resposta do serviço para %s", method)
	}
	if ok, _ := resp["ok"].(bool); !ok {
		errMsg, _ := resp["error"].(string)
		if strings.TrimSpace(errMsg) == "" {
			errMsg = fmt.Sprintf("erro no serviço (%s)", method)
		}
		return resp, fmt.Errorf("%s", errMsg)
	}
	return resp, nil
}

func (a *App) supportChatRefreshViaIPC(keepInvite bool) (supportchat.State, error) {
	method := "supportchat:refresh"
	if keepInvite {
		method = "supportchat:refresh_keeping_invite"
	}
	resp, err := a.ipcCallError(method, nil, supportChatCallWait)
	if err != nil {
		return supportchat.State{}, err
	}
	data, _ := resp["data"].(map[string]any)
	raw, err := json.Marshal(data["state"])
	if err != nil {
		return supportchat.State{}, err
	}
	var st supportchat.State
	if err := json.Unmarshal(raw, &st); err != nil {
		return supportchat.State{}, err
	}
	a.supportChatService().AdoptSnapshot(st)
	return st, nil
}

func (a *App) supportChatActiveSessionViaIPC() (string, bool, error) {
	resp, err := a.ipcCallError("supportchat:active_session", nil, supportChatCallWait)
	if err != nil {
		return "", false, err
	}
	data, _ := resp["data"].(map[string]any)
	id, _ := data["sessionId"].(string)
	has, _ := data["hasSession"].(bool)
	return id, has, nil
}

func (a *App) supportChatRespondInviteViaIPC(sessionID string, accepted bool, reason string) error {
	_, err := a.ipcCallError("supportchat:respond_invite", map[string]any{
		"sessionId": sessionID,
		"accepted":  accepted,
		"reason":    reason,
	}, supportChatCallWait)
	return err
}

func (a *App) supportChatPolicyViaIPC() (supportchat.AttachmentPolicy, error) {
	resp, err := a.ipcCallError("supportchat:policy", nil, supportChatPolicyWait)
	if err != nil {
		return supportchat.AttachmentPolicy{}, err
	}
	data, _ := resp["data"].(map[string]any)
	raw, err := json.Marshal(data["policy"])
	if err != nil {
		return supportchat.AttachmentPolicy{}, err
	}
	var policy supportchat.AttachmentPolicy
	if err := json.Unmarshal(raw, &policy); err != nil {
		return supportchat.AttachmentPolicy{}, err
	}
	return policy, nil
}
