package app

// companion_config_rpc.go — RPC "config:set" (UI companion → serviço).
//
// Decisão de arquitetura: o SERVIÇO é o dono único da config persistida
// (debug_config.json). A UI nunca grava o arquivo localmente: ela envia um
// patch semântico, o serviço valida, persiste de forma atômica (ver
// debug.Service.PersistConfig) e devolve a config EFETIVA já reaplicada.
//
// O patch é SEMÂNTICO: só os campos enviados são alterados; o restante da
// config atual é preservado. Aceita os campos de conexão do debug.Config
// (apiServer, natsServer, natsWsServer, authToken, agentId, hashes TLS etc.)
// e os runtime flags debugMode/startMinimized.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	debug "discovery/app/debug"
)

// ipcRPCConfigSet aplica um patch de configuração no serviço e devolve a
// config efetiva:
//
//	{"ok":true,"data":{...}}            — sucesso (dados já reaplicados)
//	{"ok":false,"error":"..."}          — validação falhou; NADA foi gravado
//
// payload aceita os campos no topo e/ou aninhados em "config" (o formato
// antigo do debug:set). Campos desconhecidos são ignorados para permitir
// evolução do contrato sem quebrar UIs antigas.
func (a *App) ipcRPCConfigSet(payload map[string]any) map[string]any {
	if err := a.requireDebugSvc(); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}

	patch, err := extractConfigSetPatch(payload)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	if len(patch) == 0 {
		return map[string]any{"ok": false, "error": "nenhum campo de configuração informado"}
	}
	if err := validateConfigSetPatch(patch); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}

	merged, err := mergeConfigPatch(a.DebugSvc.GetConfig(), patch)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	// SetConfig valida o conjunto final, persiste atomicamente e recarrega o
	// agentConn — reaplicação imediata, sem restart. Em erro de validação o
	// arquivo não é tocado.
	if err := a.DebugSvc.SetConfig(merged); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}

	if v, ok := patch["debugMode"]; ok {
		a.RuntimeFlags.DebugMode = v.(bool)
	}
	if v, ok := patch["startMinimized"]; ok {
		a.RuntimeFlags.StartMinimized = v.(bool)
	}

	data, err := configSetEffectiveData(a.DebugSvc.GetConfig(), a.RuntimeFlags)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true, "data": data}
}

// StartDebugConfigWatcher liga o watcher de alteração externa do
// debug_config.json e registra o hook que reaplica os efeitos no App
// (invalida cache de identidade, recarrega o agentConn e notifica as UIs via
// "debug:config_updated"). Deve ser chamado no startup do processo que roda o
// core; é idempotente. Sem esse registro o watcher apenas atualiza o estado
// do serviço (comportamento previsto como fallback).
func (a *App) StartDebugConfigWatcher() {
	if a == nil || a.DebugSvc == nil {
		return
	}
	a.DebugSvc.SetExternalConfigApplyHook(func(cfg DebugConfig) {
		// AgentInfo é um valor (cache zero-safe): invalidação sempre possível.
		a.AgentInfo.Invalidate()
		if a.AgentConn != nil {
			a.AgentConn.Reload()
		}
		a.EmitEvent("debug:config_updated", "config", cfg)
	})
	a.DebugSvc.StartWatcher()
}

func extractConfigSetPatch(payload map[string]any) (map[string]any, error) {
	patch := make(map[string]any)
	if payload == nil {
		return patch, nil
	}
	if raw, ok := payload["config"]; ok {
		nested, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("campo config inválido: esperado objeto")
		}
		for key, value := range nested {
			patch[key] = value
		}
	}
	for key, value := range payload {
		if key == "config" || key == "method" {
			continue
		}
		patch[key] = value
	}
	return patch, nil
}

var configSetStringFields = map[string]bool{
	"apiScheme":              true,
	"apiServer":              true,
	"authToken":              true,
	"natsServer":             true,
	"natsWsServer":           true,
	"natsServerHost":         true,
	"natsServerHostInternal": true,
	"apiTlsCertHash":         true,
	"natsTlsCertHash":        true,
	"agentId":                true,
	"scheme":                 true,
	"server":                 true,
}

var configSetBoolFields = map[string]bool{
	"debugMode":                true,
	"startMinimized":           true,
	"allowInsecureTls":         true,
	"apiInsecure":              true,
	"natsUseWssExternal":       true,
	"enforceTlsHashValidation": true,
	"handshakeEnabled":         true,
}

func configSetKnownField(key string) bool {
	if configSetStringFields[key] || configSetBoolFields[key] {
		return true
	}
	return key == "automationP2pWingetInstallEnabled"
}

// validateConfigSetPatch valida tipos e semântica ANTES de qualquer escrita.
func validateConfigSetPatch(patch map[string]any) error {
	for key, value := range patch {
		switch {
		case configSetBoolFields[key]:
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("campo %s inválido: esperado boolean", key)
			}
		case configSetStringFields[key]:
			if _, ok := value.(string); !ok {
				return fmt.Errorf("campo %s inválido: esperado string", key)
			}
		case key == "automationP2pWingetInstallEnabled":
			if value == nil {
				continue
			}
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("campo %s inválido: esperado boolean", key)
			}
		}
	}

	if raw, ok := patch["apiServer"]; ok {
		if err := validateConfigEndpoint("apiServer", raw.(string), "http", "https"); err != nil {
			return err
		}
	}
	if raw, ok := patch["natsServer"]; ok {
		if err := validateConfigEndpoint("natsServer", raw.(string), "nats", "tls", "ws", "wss"); err != nil {
			return err
		}
	}
	if raw, ok := patch["natsWsServer"]; ok {
		if err := validateConfigEndpoint("natsWsServer", raw.(string), "ws", "wss"); err != nil {
			return err
		}
	}
	if raw, ok := patch["apiScheme"]; ok {
		scheme := strings.ToLower(strings.TrimSpace(raw.(string)))
		if scheme != "" && scheme != "http" && scheme != "https" {
			return fmt.Errorf("apiScheme inválido: use 'http' ou 'https'")
		}
	}
	if raw, ok := patch["agentId"]; ok {
		agentID := strings.TrimSpace(raw.(string))
		if agentID != "" && !debug.IsValidAgentID(agentID) {
			return fmt.Errorf("agentId inválido: informe um GUID válido")
		}
	}
	if raw, ok := patch["authToken"]; ok {
		if strings.TrimSpace(raw.(string)) == "" {
			return fmt.Errorf("authToken inválido: não pode ser vazio")
		}
	}
	return nil
}

func validateConfigEndpoint(field, value string, allowedSchemes ...string) error {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return nil
	}
	if strings.ContainsAny(raw, " \t\r\n") {
		return fmt.Errorf("campo %s inválido: contém espaços", field)
	}
	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err != nil {
			return fmt.Errorf("campo %s inválido: %w", field, err)
		}
		scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
		allowed := false
		for _, candidate := range allowedSchemes {
			if scheme == candidate {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("campo %s inválido: scheme %q não permitido (use %s)", field, scheme, strings.Join(allowedSchemes, "/"))
		}
		if strings.TrimSpace(parsed.Host) == "" {
			return fmt.Errorf("campo %s inválido: host ausente", field)
		}
		return nil
	}
	// Sem scheme: host:porta (formato do formulário de Debug).
	if strings.Contains(raw, "/") || strings.HasPrefix(raw, ":") {
		return fmt.Errorf("campo %s inválido: informe host:porta ou URL completa", field)
	}
	return nil
}

// mergeConfigPatch parte da config EFETIVA atual e sobrescreve apenas as
// chaves enviadas — patch parcial nunca apaga campos não enviados.
func mergeConfigPatch(current debug.Config, patch map[string]any) (debug.Config, error) {
	rawCurrent, err := json.Marshal(current)
	if err != nil {
		return debug.Config{}, fmt.Errorf("falha ao ler config atual: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rawCurrent, &fields); err != nil {
		return debug.Config{}, fmt.Errorf("falha ao ler config atual: %w", err)
	}
	for key, value := range patch {
		if !configSetKnownField(key) {
			continue
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return debug.Config{}, fmt.Errorf("campo %s inválido: %w", key, err)
		}
		fields[key] = raw
	}
	mergedRaw, err := json.Marshal(fields)
	if err != nil {
		return debug.Config{}, fmt.Errorf("falha ao montar config: %w", err)
	}
	var merged debug.Config
	if err := json.Unmarshal(mergedRaw, &merged); err != nil {
		return debug.Config{}, fmt.Errorf("config inválida: %w", err)
	}
	return merged, nil
}

func configSetEffectiveData(cfg debug.Config, flags RuntimeFlags) (map[string]any, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	data := make(map[string]any)
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	data["debugMode"] = flags.DebugMode
	data["startMinimized"] = flags.StartMinimized
	return data, nil
}
