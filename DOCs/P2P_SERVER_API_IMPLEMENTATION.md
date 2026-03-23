# Guia de Implementação — API P2P no Servidor

Este documento é o **guia normativo para o backend** receber e processar os dados
enviados pelo agente Discovery nas rotas de coordenação de enxame P2P.  
Cobre esquema completo de cada endpoint, regras de validação, modelo de dados
sugerido, comportamento a retornar e orientações de operação (rate limiting,
auditoria, retenção).

---

## Índice

1. [Autenticação](#1-autenticação)
2. [GET /api/agent-auth/me/p2p-seed-plan](#2-get-apíagent-authme-p2p-seed-plan)
3. [POST /api/agent-auth/me/p2p-telemetry](#3-post-apiagent-authme-p2p-telemetry)
4. [GET /api/agent-auth/me/p2p-distribution-status](#4-get-apiagent-authme-p2p-distribution-status)
5. [Esquema de banco de dados sugerido](#5-esquema-de-banco-de-dados-sugerido)
6. [Regras de validação consolidadas](#6-regras-de-validação-consolidadas)
7. [Rate limiting](#7-rate-limiting)
8. [Retenção e limpeza](#8-retenção-e-limpeza)
9. [Códigos de erro padrão](#9-códigos-de-erro-padrão)

---

## 1. Autenticação

Todos os endpoints usam **Bearer token** por agente, idêntico ao mecanismo
usado nos demais endpoints `/api/agent-auth/me/…`.

```
Authorization: Bearer <agentAuthToken>
```

O servidor deve:
- Validar a assinatura/expiry do token antes de processar qualquer payload.
- Resolver o `agentId` e o `siteId` a partir do token — **não confiar** nos
  campos `agentId` / `siteId` do corpo da requisição como fonte primária;
  usá-los apenas como informação auxiliar/display e validar que não divergem
  do token.
- Retornar `401 Unauthorized` para token ausente, inválido ou expirado.
- Retornar `403 Forbidden` quando o agente existe mas não tem permissão para
  o recurso (ex.: agente de outro tenant tentando acessar status de outro site).

---

## 2. GET /api/agent-auth/me/p2p-seed-plan

### Propósito

Retorna a recomendação de quantos agentes do site devem atuar como seeders
(baixam do servidor central e redistribuem via P2P aos demais).  
O agente faz cache local desse resultado por **5 minutos** antes de
repetir a chamada.

### Request

```
GET /api/agent-auth/me/p2p-seed-plan
Authorization: Bearer <token>
```

Sem query params, sem body.

### Response `200 OK`

```json
{
  "siteId":         "uuid-do-site",
  "generatedAtUtc": "2026-03-23T12:00:00Z",
  "plan": {
    "totalAgents":       50,
    "configuredPercent": 10,
    "minSeeds":          2,
    "selectedSeeds":     5
  }
}
```

#### Campos da resposta

| Campo | Tipo | Obrigatório | Descrição |
|-------|------|:-----------:|-----------|
| `siteId` | string (UUID) | Não | Site ao qual o agente pertence. Omitir em deployments single-site. |
| `generatedAtUtc` | string RFC3339 | Não | Momento em que o servidor calculou o plano. Preenchido pelo servidor. |
| `plan.totalAgents` | int ≥ 0 | **Sim** | Total de agentes online/ativos no site nas últimas 24h. |
| `plan.configuredPercent` | int 0–100 | **Sim** | Percentual alvo de agentes que devem ser seeders. |
| `plan.minSeeds` | int ≥ 1 | **Sim** | Mínimo absoluto de seeders independentemente do percentual. |
| `plan.selectedSeeds` | int ≥ 0 | **Sim** | Valor resolvido: `max(ceil(totalAgents × configuredPercent / 100), minSeeds)`, limitado a `totalAgents`. |

#### Regras de cálculo obrigatórias

```
selectedSeeds = max(ceil(totalAgents × configuredPercent / 100), minSeeds)
selectedSeeds = min(selectedSeeds, totalAgents)
```

Se `totalAgents == 0`, retornar `selectedSeeds = 0`.

#### Comportamento do agente quando o plano está parcial/zerado

O agente já possui fallback local: se `plan.selectedSeeds == 0` e
`plan.totalAgents == 0`, ele calcula localmente usando sua própria configuração.
O servidor **pode** retornar esses zeros válidos quando ainda não há dados de
fleet disponíveis.

#### Códigos de status

| Código | Situação |
|--------|----------|
| `200` | Plano retornado normalmente |
| `401` | Token inválido/expirado |
| `404` | Agente desconhecido (não registrado) |
| `500` | Erro interno calculando plano |

---

## 3. POST /api/agent-auth/me/p2p-telemetry

### Propósito

Recebe métricas de saúde do enxame enviadas pelo agente a cada **5 minutos**.
Usado para dashboards operacionais, alertas e auditoria de transferências P2P.

### Request

```
POST /api/agent-auth/me/p2p-telemetry
Authorization: Bearer <token>
Content-Type: application/json
```

#### Body

```json
{
  "agentId":       "agent-abc123",
  "siteId":        "uuid-do-site",
  "collectedAtUtc": "2026-03-23T12:00:00Z",
  "metrics": {
    "publishedArtifacts":    3,
    "replicationsStarted":  12,
    "replicationsSucceeded":11,
    "replicationsFailed":    1,
    "bytesServed":      157286400,
    "bytesDownloaded":   52428800,
    "queuedReplications":    0,
    "activeReplications":    1,
    "autoDistributionRuns":  6,
    "catalogRefreshRuns":    6,
    "chunkedDownloads":      2,
    "chunksDownloaded":     14
  },
  "currentSeedPlan": {
    "totalAgents":       50,
    "configuredPercent": 10,
    "minSeeds":          2,
    "selectedSeeds":     5
  }
}
```

#### Definição dos campos

| Campo | Tipo | Obrigatório | Validação | Descrição |
|-------|------|:-----------:|-----------|-----------|
| `agentId` | string | Não¹ | Máx. 128 chars, alphanumeric/dash/underscore | ID do agente. O servidor resolve via token; este campo é auxiliar. |
| `siteId` | string | Não¹ | UUID ou string curta ≤ 64 chars | Site do agente. Idem acima. |
| `collectedAtUtc` | string RFC3339 | **Sim** | Formato RFC3339 estrito; não pode ser no futuro (+5min tolerância); não pode ser mais de 24h no passado | Timestamp de coleta das métricas no agente |
| `metrics` | object | **Sim** | Ver abaixo | Counters cumulativos desde o início do processo |
| `currentSeedPlan` | object | **Sim** | Todos os campos do plano ≥ 0 | Plano de seed ativo no momento da coleta |

**¹** Resolve do token; se informado e divergir do token, retornar `400`.

#### Campos de `metrics`

| Campo | Tipo | Mín. | Máx. | Descrição |
|-------|------|-----:|-----:|-----------|
| `publishedArtifacts` | int | 0 | 100 000 | Artifacts publicados no cache local |
| `replicationsStarted` | int | 0 | 10 000 000 | Réplicas push/pull iniciadas |
| `replicationsSucceeded` | int | 0 | 10 000 000 | Réplicas concluídas com sucesso |
| `replicationsFailed` | int | 0 | 10 000 000 | Réplicas que falharam |
| `bytesServed` | int64 | 0 | 1 TB (1 099 511 627 776) | Bytes servidos a peers |
| `bytesDownloaded` | int64 | 0 | 1 TB | Bytes baixados de peers |
| `queuedReplications` | int | 0 | 1 000 | Réplicas pendentes na fila no momento |
| `activeReplications` | int | 0 | 64 | Workers de replicação ativos no momento |
| `autoDistributionRuns` | int | 0 | 10 000 000 | Quantidade de ticks de autodistribuição |
| `catalogRefreshRuns` | int | 0 | 10 000 000 | Quantas vezes o catálogo foi sincronizado com peers |
| `chunkedDownloads` | int | 0 | 10 000 000 | Número de downloads em modo swarm (multi-chunk) concluídos |
| `chunksDownloaded` | int64 | 0 | 10 000 000 000 | Total de chunks individuais baixados |

> **Nota contadores cumulativos:** Os valores são acumulados desde o início do
> processo, não deltas. O servidor deve calcular deltas ao persistir (subtrair
> o valor anterior para a mesma `agentId`), armazenando tanto o cumulativo
> quanto o delta no período para facilitar queries de dashboards.

#### Campos de `currentSeedPlan`

Mesma estrutura do response do `GET /p2p-seed-plan`. Todos inteiros ≥ 0.  
`selectedSeeds ≤ totalAgents` (validar; rejeitar se violar).

### Response

| Código | Corpo | Significado |
|--------|-------|-------------|
| `202 Accepted` | `{"received": true}` (opcional) | **Recomendado.** Payload aceito para processamento assíncrono. |
| `200 OK` | idem | Aceito se processamento síncrono. |
| `400 Bad Request` | `{"error": "mensagem"}` | Payload inválido (ver §6) |
| `401` | — | Token inválido |
| `429 Too Many Requests` | `{"error":"rate limit exceeded", "retryAfterSeconds": 60}` | Rate limit atingido (ver §7) |
| `500` | — | Erro interno |

> Usar **`202`** é preferível: o agente não precisa aguardar persistência. O
> agente ignora o corpo da resposta — apenas verifica o código de status para
> logar falha.

---

## 4. GET /api/agent-auth/me/p2p-distribution-status

### Propósito

Retorna visibilidade de distribuição por artifact para o dashboard de operações.
O servidor agrega dados recebidos via telemetria de todos os agentes do site.

### Request

```
GET /api/agent-auth/me/p2p-distribution-status
Authorization: Bearer <token>
```

#### Query params opcionais

| Param | Tipo | Default | Descrição |
|-------|------|---------|-----------|
| `artifactId` | string | — | Filtra por um artifact específico |
| `limit` | int 1–500 | 100 | Página de resultados |
| `offset` | int ≥ 0 | 0 | Offset para paginação |

### Response `200 OK`

```json
[
  {
    "artifactId":     "name:myapp-1.2.3.exe",
    "artifactName":   "MyApp-1.2.3.exe",
    "peerCount":      8,
    "peerAgentIds":   ["agent-1", "agent-2", "agent-3"],
    "lastUpdatedUtc": "2026-03-23T11:55:00Z"
  }
]
```

#### Campos da resposta (por item do array)

| Campo | Tipo | Obrigatório | Validação no servidor ao gravar | Descrição |
|-------|------|:-----------:|----------------------------------|-----------|
| `artifactId` | string | **Sim** | Não vazio, ≤ 512 chars. **Rejeitar entrada sem artifactId** (ver §6.1) | ID canônico do artifact |
| `artifactName` | string | Não | Máx. 260 chars; sem path separators (`/`, `\`, `..`) | Nome de arquivo legível |
| `peerCount` | int | **Sim** | ≥ 0; consistente com `len(peerAgentIds)` quando ambos fornecidos | Contagem de peers com o artifact em cache |
| `peerAgentIds` | string[] | Não | Máx. **500 itens**; cada string ≤ 128 chars | Lista de agentIds que possuem o artifact |
| `lastUpdatedUtc` | string RFC3339 | Não | Formato RFC3339 | Última vez que o servidor atualizou o contador |

**O campo `lastUpdatedUtc` é preenchido pelo servidor** na resposta, refletindo
quando foi a última vez que um agente reportou ter esse artifact. O cliente não
precisa enviá-lo; se enviado, é ignorado.

#### Comportamento de agregação

O servidor constrói esta visão a partir dos payloads de telemetria:
1. Para cada `agentId` + `artifactId` vistos na telemetria, registrar presença.
2. `peerCount` = número de agentIds únicos que reportaram ter o artifact nas
   últimas **2 horas** (TTL configurável).
3. `peerAgentIds` = lista desses agentIds (ocultar se `peerCount > 500` para
   não explodir payloads — retornar apenas `peerCount`).
4. `lastUpdatedUtc` = timestamp mais recente dentre os agentes que reportaram.

---

## 5. Esquema de banco de dados sugerido

### Tabela: `p2p_agent_telemetry`

Armazena os snapshots recebidos por agente. Uma linha por POST aceito.

```sql
CREATE TABLE p2p_agent_telemetry (
    id                      BIGSERIAL PRIMARY KEY,
    agent_id                VARCHAR(128)  NOT NULL,
    site_id                 VARCHAR(64),
    collected_at            TIMESTAMPTZ   NOT NULL,
    received_at             TIMESTAMPTZ   NOT NULL DEFAULT now(),

    -- métricas cumulativas (snapshot)
    published_artifacts     INT           NOT NULL DEFAULT 0,
    replications_started    BIGINT        NOT NULL DEFAULT 0,
    replications_succeeded  BIGINT        NOT NULL DEFAULT 0,
    replications_failed     BIGINT        NOT NULL DEFAULT 0,
    bytes_served            BIGINT        NOT NULL DEFAULT 0,
    bytes_downloaded        BIGINT        NOT NULL DEFAULT 0,
    queued_replications     INT           NOT NULL DEFAULT 0,
    active_replications     INT           NOT NULL DEFAULT 0,
    auto_distribution_runs  BIGINT        NOT NULL DEFAULT 0,
    catalog_refresh_runs    BIGINT        NOT NULL DEFAULT 0,
    chunked_downloads       BIGINT        NOT NULL DEFAULT 0,
    chunks_downloaded       BIGINT        NOT NULL DEFAULT 0,

    -- seed plan vigente
    plan_total_agents       INT           NOT NULL DEFAULT 0,
    plan_configured_percent INT           NOT NULL DEFAULT 0,
    plan_min_seeds          INT           NOT NULL DEFAULT 0,
    plan_selected_seeds     INT           NOT NULL DEFAULT 0
);

CREATE INDEX idx_p2p_telemetry_agent_time ON p2p_agent_telemetry (agent_id, collected_at DESC);
CREATE INDEX idx_p2p_telemetry_site_time  ON p2p_agent_telemetry (site_id, collected_at DESC);
```

### Tabela: `p2p_artifact_presence`

Mantém quais agentes têm qual artifact em cache (atualizada via telemetria).

```sql
CREATE TABLE p2p_artifact_presence (
    artifact_id     VARCHAR(512)  NOT NULL,
    artifact_name   VARCHAR(260),
    agent_id        VARCHAR(128)  NOT NULL,
    site_id         VARCHAR(64),
    last_seen_at    TIMESTAMPTZ   NOT NULL DEFAULT now(),

    PRIMARY KEY (artifact_id, agent_id)
);

CREATE INDEX idx_p2p_presence_artifact ON p2p_artifact_presence (artifact_id, last_seen_at DESC);
CREATE INDEX idx_p2p_presence_site     ON p2p_artifact_presence (site_id, last_seen_at DESC);
```

> A tabela `p2p_artifact_presence` é upsert-only. Nunca deletar manualmente;
> o job de limpeza (§8) remove linhas com `last_seen_at` expirado.

### Tabela: `p2p_seed_plan`

Plano calculado por site, revisado periodicamente pelo servidor.

```sql
CREATE TABLE p2p_seed_plan (
    site_id             VARCHAR(64)  NOT NULL PRIMARY KEY,
    total_agents        INT          NOT NULL DEFAULT 0,
    configured_percent  INT          NOT NULL DEFAULT 10,
    min_seeds           INT          NOT NULL DEFAULT 2,
    selected_seeds      INT          NOT NULL DEFAULT 0,
    generated_at        TIMESTAMPTZ  NOT NULL DEFAULT now()
);
```

---

## 6. Regras de validação consolidadas

### 6.1 `artifactId` obrigatório em status por artifact

**Regra:** Ao processar telemetria ou atualizar `p2p_artifact_presence`, se um
artifact não tiver `artifactId` definido (nulo, vazio ou ausente), o servidor
deve **silenciosamente descartar** aquele artifact específico do registro de
presença. Não deve rejeitar o payload inteiro — apenas omitir o artifact
problemático e logar um contador de métrica (`p2p.telemetry.artifact_skipped_no_id`).

**Motivação:** O agente emite `artifactId` canônico na forma `name:<filename>`
como fallback quando não há ID explícito. O servidor deve aceitar esse formato
e tratá-lo como ID válido.  
Um `artifactId` do formato `name:…` é válido mas indica ausência de ID
originado em catálogo — o servidor pode marcar esses registros com um flag
`id_is_synthetic = true` para distinção em relatórios.

### 6.2 Validação RFC3339 em campos `*Utc` / `*At`

Campos obrigatórios: `collectedAtUtc`.  
Campos opcionais: `generatedAtUtc`, `lastUpdatedUtc`.

Regras:
- Deve ser parseável como RFC3339 (com timezone explícito — `Z` ou `±HH:MM`).
- `collectedAtUtc` não pode ser no **futuro** (tolerância de +5 minutos para
  clock skew).
- `collectedAtUtc` não pode ser mais de **24 horas** no passado. Payloads mais
  antigos indicam agente preso/bugado; rejeitar com `400` e mensagem:
  `"collectedAtUtc demasiado antigo (máx. 24h)"`.

### 6.3 Limites de `peerAgentIds`

| Regra | Valor |
|-------|-------|
| Máx. itens no array | 500 |
| Máx. comprimento de cada item | 128 chars |
| Caracteres permitidos por item | `[a-zA-Z0-9\-_.]` |

Se `len(peerAgentIds) > 500`, rejeitar com `400`:  
`"peerAgentIds excede o limite de 500 itens"`.

### 6.4 Limites de `artifactName`

| Regra | Valor |
|-------|-------|
| Comprimento máximo | 260 chars |
| Proibido | `/`, `\`, `..`, caracteres de controle (< 0x20) |
| Proibido | Strings que resultam em path traversal após normalização |

### 6.5 Consistência de counters

- Todos os campos de `metrics` devem ser `≥ 0`. Valores negativos → `400`.
- `replicationsSucceeded + replicationsFailed ≤ replicationsStarted + 1`  
  (tolerância de +1 por race condition de leitura). Violação → `400`.
- `activeReplications ≤ 64`.
- `queuedReplications ≤ 1 000`.
- `bytes_served` e `bytes_downloaded` ≤ 1 TiB (`1_099_511_627_776`).

### 6.6 Divergência de `agentId` / `siteId` no body vs token

Se o campo `agentId` ou `siteId` estiver presente no body **e** divergir do
valor resolvido pelo token → rejeitar com `400`:  
`"agentId no payload não corresponde ao token"`.

---

## 7. Rate limiting

### Endpoint de telemetria

| Parâmetro | Valor recomendado |
|-----------|:-----------------:|
| Janela | 10 minutos |
| Limite por `agentId` | 5 requisições |
| Burst | 2 requisições extras |
| Header de resposta ao exceder | `Retry-After: <segundos>` |
| Código HTTP | `429 Too Many Requests` |

**Motivação:** O agente envia de 5 em 5 minutos → o legítimo envia no máximo 2
por janela de 10min. O limite de 5 acomoda retries em caso de falha transiente
sem abrir espaço para flood acidental.

Body da resposta `429`:
```json
{
  "error": "rate limit de telemetria excedido",
  "retryAfterSeconds": 300
}
```

### Endpoint de seed-plan

| Parâmetro | Valor recomendado |
|-----------|:-----------------:|
| Janela | 5 minutos |
| Limite por `agentId` | 3 requisições |

### Endpoint de distribution-status

| Parâmetro | Valor recomendado |
|-----------|:-----------------:|
| Janela | 1 minuto |
| Limite por `agentId` | 30 requisições |
| Burst | 10 |

Este endpoint é consultado por dashboards (leitura); limite mais alto.

---

## 8. Retenção e limpeza

### Telemetria histórica (`p2p_agent_telemetry`)

| Categoria | Retenção |
|-----------|----------|
| Dados detalhados por agente | 90 dias |
| Agregado diário por site | 1 ano |
| Agregado mensal | indefinido |

Sugestão: job diário que:
1. Agrega linhas com mais de 7 dias em tabela `p2p_telemetry_daily_agg`.
2. Delete linhas detalhadas com mais de 90 dias.

### Presença de artifacts (`p2p_artifact_presence`)

Linha expirada = `last_seen_at < now() - interval '2 hours'`  
(configurável; deve ser ≥ 2× o intervalo de telemetria do agente).

Job de limpeza — executar a cada 15 minutos:
```sql
DELETE FROM p2p_artifact_presence
WHERE last_seen_at < now() - INTERVAL '2 hours';
```

### Plano de seed (`p2p_seed_plan`)

Recalcular por site a cada 15 minutos com base em contagem de agentes
ativos (definição: `received_at > now() - interval '10 minutes'` na tabela
de telemetria).

---

## 9. Códigos de erro padrão

Todos os erros devem retornar `Content-Type: application/json` com body:

```json
{
  "error": "descrição humana do problema",
  "field": "nomeDoCampo",     // opcional, quando o erro é específico de campo
  "code":  "CODIGO_MAQUINA"  // opcional, para tratamento programático
}
```

### Códigos de máquina (`code`) sugeridos

| `code` | Situação |
|--------|----------|
| `INVALID_RFC3339` | Campo `*Utc` / `*At` com formato inválido |
| `TIMESTAMP_TOO_OLD` | `collectedAtUtc` > 24h no passado |
| `TIMESTAMP_IN_FUTURE` | `collectedAtUtc` > agora + 5min |
| `METRIC_NEGATIVE` | Qualquer campo de `metrics` < 0 |
| `METRIC_INCONSISTENT` | `succeeded + failed > started + 1` |
| `ARTIFACT_ID_MISSING` | `artifactId` vazio em item de status |
| `ARTIFACT_NAME_INVALID` | `artifactName` contém path traversal ou chars proibidos |
| `PEER_LIST_TOO_LARGE` | `peerAgentIds` > 500 itens |
| `AGENT_ID_MISMATCH` | `agentId` no body diverge do token |
| `RATE_LIMIT_EXCEEDED` | Rate limit atingido |

---

## Apêndice A — Fluxo de dados típico

```
Agente (a cada 5min)
  │
  ├─► GET  /p2p-seed-plan        ← obtém plano → usa para decidir se é seeder
  │
  └─► POST /p2p-telemetry        ← envia métricas cumulativas do período
        │
        └─► Servidor persiste em p2p_agent_telemetry
            └─► Upsert em p2p_artifact_presence por cada artifact em cache

Dashboard / Ops (on demand)
  │
  └─► GET  /p2p-distribution-status  ← agrega p2p_artifact_presence por site
```

## Apêndice B — Exemplo completo de payload de telemetria inválido e resposta

**Request (inválido):**
```json
{
  "agentId": "agent-x",
  "collectedAtUtc": "23/03/2026 12:00:00",
  "metrics": {
    "replicationsSucceeded": 10,
    "replicationsStarted":   3
  },
  "currentSeedPlan": {
    "totalAgents": 50,
    "selectedSeeds": 60
  }
}
```

**Response `400 Bad Request`:**
```json
{
  "error": "múltiplos erros de validação",
  "details": [
    { "field": "collectedAtUtc", "code": "INVALID_RFC3339",    "message": "formato inválido; esperado RFC3339 com timezone" },
    { "field": "metrics",        "code": "METRIC_INCONSISTENT","message": "replicationsSucceeded (10) > replicationsStarted (3)" },
    { "field": "currentSeedPlan","code": "METRIC_INCONSISTENT","message": "selectedSeeds (60) > totalAgents (50)" }
  ]
}
```
