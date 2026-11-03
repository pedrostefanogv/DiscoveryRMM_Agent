package automation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Cliente HTTP dos sensores (agent-auth).
//
// Endpoints do contrato (Discovery.Core/DTOs/SensorDtos.cs):
//   POST /api/v1/agent-auth/me/sensors/sync     -> AgentSensorSyncResponse
//   POST /api/v1/agent-auth/me/sensors/samples  -> AgentSensorResultResponse
//
// Mesmos headers canônicos da automação (setAutomationHeaders) e mesmos
// timeouts do policy-sync/callbacks: 45s no sync (payload com scripts) e 30s no
// envio de amostras.
// ─────────────────────────────────────────────────────────────────────────────

const (
	sensorSyncTimeout    = 45 * time.Second
	sensorSamplesTimeout = 30 * time.Second
)

// errSensorSyncNotFound indica que o servidor não expõe o endpoint de sensores
// (404 — backend anterior ao recurso). Não é falha de rede: o loop de sensores
// fica desabilitado e tenta de novo com backoff longo.
var errSensorSyncNotFound = errors.New("sensor sync indisponivel no servidor (404)")

// sensorBatchRejectedError indica que o servidor RECUSOU o lote por conteúdo
// inválido (HTTP 400). Reenviar o mesmo payload não resolve: o lote é
// descartado pelo agente em vez de virar poison pill na outbox.
type sensorBatchRejectedError struct {
	status  int
	message string
}

func (e *sensorBatchRejectedError) Error() string {
	if e.message == "" {
		return fmt.Sprintf("lote de sensores rejeitado (HTTP %d)", e.status)
	}
	return fmt.Sprintf("lote de sensores rejeitado (HTTP %d): %s", e.status, e.message)
}

// isSensorBatchRejected informa se o erro é rejeição definitiva do lote.
func isSensorBatchRejected(err error) bool {
	var rejected *sensorBatchRejectedError
	return errors.As(err, &rejected)
}

// SyncSensors busca a policy de sensores. Devolve errSensorSyncNotFound (via
// errors.Is) quando o servidor não tem o recurso.
func (c *Client) SyncSensors(ctx context.Context, cfg RuntimeConfig, reqBody SensorSyncRequest, correlationID string) (*SensorSyncResponse, error) {
	baseURL, err := normalizeBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("falha ao serializar sensor sync: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, sensorSyncTimeout)
	defer cancel()

	endpoint := baseURL + "/api/v1/agent-auth/me/sensors/sync"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("falha ao criar request de sensor sync: %w", err)
	}
	if err := setAutomationHeaders(req, cfg, correlationID); err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("falha ao chamar sensor sync: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("falha ao ler resposta de sensor sync: %w", err)
	}

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, errSensorSyncNotFound
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("nao autorizado (401): verifique token do agente")
	case resp.StatusCode == http.StatusBadRequest:
		return nil, fmt.Errorf("sensor sync rejeitado (400): %s", strings.TrimSpace(string(body)))
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, fmt.Errorf("sensor sync retornou status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result SensorSyncResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("falha ao decodificar resposta de sensor sync: %w", err)
	}
	if result.SensorCount == 0 && len(result.Sensors) > 0 {
		result.SensorCount = len(result.Sensors)
	}
	return &result, nil
}

// ReportSensorSamples envia um lote de amostras (≤100).
//
// 400 vira sensorBatchRejectedError (definitivo); o resto é retentável pela
// outbox (automation_callback_queue).
func (c *Client) ReportSensorSamples(ctx context.Context, cfg RuntimeConfig, batch SensorResultBatch, correlationID string) (*SensorResultResponse, error) {
	baseURL, err := normalizeBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	if len(batch.Samples) == 0 {
		return &SensorResultResponse{}, nil
	}
	if strings.TrimSpace(batch.AgentID) == "" {
		batch.AgentID = cfg.AgentID
	}

	payload, err := json.Marshal(batch)
	if err != nil {
		return nil, fmt.Errorf("falha ao serializar amostras de sensores: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, sensorSamplesTimeout)
	defer cancel()

	endpoint := baseURL + "/api/v1/agent-auth/me/sensors/samples"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("falha ao criar request de amostras de sensores: %w", err)
	}
	if err := setAutomationHeaders(req, cfg, correlationID); err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("falha ao enviar amostras de sensores: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("falha ao ler resposta de amostras de sensores: %w", err)
	}

	switch {
	case resp.StatusCode == http.StatusBadRequest:
		return nil, &sensorBatchRejectedError{status: resp.StatusCode, message: strings.TrimSpace(string(body))}
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("nao autorizado (401): verifique token do agente")
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("amostras de sensores: endpoint nao encontrado (404)")
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return nil, fmt.Errorf("amostras de sensores retornou status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result SensorResultResponse
	if len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("falha ao decodificar resposta de amostras de sensores: %w", err)
		}
	}
	return &result, nil
}
