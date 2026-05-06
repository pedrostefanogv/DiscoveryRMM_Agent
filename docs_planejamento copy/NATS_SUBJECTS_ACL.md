# NATS Subjects + ACL - Padrao Operacional DiscoveryRMM

> Versao: 1.1.0 | Data: 2026-05-05
> Escopo: Servidor (C#), Agent (Go), Frontend (JS/TS)
> Fonte de verdade complementar: CONTRATO_COMUNICACAO_REALTIME.md (v4.2.0)

---

## 0. Changelog

| Versao | Data | Mudancas |
|---|---|---|
| 1.1.0 | 2026-05-05 | Inclusao de comando em massa por escopo (`site`, `client`, `global`), envelope de dispatch com idempotencia e ACL dedicada para fan-out sem loop agente-a-agente. |
| 1.0.0 | 2026-05-05 | Versao inicial consolidada de subjects e ACL. |

---

## 1. Objetivo

Este documento consolida:

1. Todos os subjects que podem ser publicados/consumidos.
2. O contrato de payload por subject.
3. O projeto de ACL por perfil de usuario para NATS WS.
4. As regras para o frontend consumir eventos corretamente por escopo.

---

## 2. Matriz completa de subjects

| Subject | Direcao | Publisher | Subscriber | Payload |
|---|---|---|---|---|
| `tenant.{c}.site.{s}.agent.{a}.heartbeat` | Agent -> Servidor | Agent | Servidor | `AgentHeartbeat` |
| `tenant.{c}.site.{s}.agent.{a}.command` | Servidor -> Agent | Servidor | Agent | `CommandEnvelope` |
| `tenant.{c}.site.{s}.agents.command` | Servidor -> Agents do site | Servidor | Todos os agents do site | `CommandDispatchEnvelope` |
| `tenant.{c}.agents.command` | Servidor -> Agents do cliente | Servidor | Todos os agents do cliente | `CommandDispatchEnvelope` |
| `tenant.global.agents.command` | Servidor -> Todos os agents | Servidor | Todos os agents | `CommandDispatchEnvelope` |
| `tenant.global.pong` | Servidor -> Todos os agents | Servidor | Todos os agents | `GlobalPongMessage` |
| `tenant.{c}.site.{s}.agent.{a}.result` | Agent -> Servidor | Agent | Servidor | `CommandResult` |
| `tenant.{c}.site.{s}.agent.{a}.sync.ping` | Servidor -> Agent | Servidor | Agent | `SyncInvalidationPingMessage` |
| `tenant.{c}.site.{s}.agent.{a}.remote-debug.log` | Agent -> Servidor | Agent | Servidor | `RemoteDebugLogEntry` |
| `tenant.{c}.site.{s}.agent.{a}.hardware` | Agent -> Servidor | Agent | Servidor | `HardwareReport` |
| `tenant.{c}.site.{s}.p2p.discovery` | Servidor -> Agents | Servidor | Agents do site | `P2pDiscoverySnapshot` |
| `tenant.{c}.site.{s}.dashboard.events` | Servidor/Agent -> Consumidores | Servidor e Agent | Frontend (WS) e consumidores internos | `DashboardEvent<T>` |
| `tenant.{c}.dashboard.events` | Servidor -> Consumidores | Servidor | Frontend (WS) e consumidores internos | `DashboardEvent<T>` (fallback por cliente) |
| `tenant.unscoped.dashboard.events` | Servidor -> Consumidores | Servidor | Frontend (WS) e consumidores internos | `DashboardEvent<T>` (fallback sem tenant) |
| `tenant.{c}.site.{s}.agent.{a}.dashboard.events` | legado/transicao | legado | nao usar na UI | legado; convergir para `tenant.{c}.site.{s}.dashboard.events` |

---

## 3. Contrato de payload por subject

### 3.1 Heartbeat (`tenant.{c}.site.{s}.agent.{a}.heartbeat`)

Campos:

- `agentId` (Guid, obrigatorio)
- `clientId` (Guid?, opcional)
- `siteId` (Guid?, opcional)
- `ipAddress` (string?, opcional)
- `hostname` (string?, opcional)
- `agentVersion` (string?, opcional)
- `timestampUtc` (DateTime?, opcional)
- `cpuPercent` (double?, opcional)
- `memoryPercent` (double?, opcional)
- `memoryTotalGb` (double?, opcional)
- `memoryUsedGb` (double?, opcional)
- `diskPercent` (double?, opcional)
- `diskTotalGb` (double?, opcional)
- `diskUsedGb` (double?, opcional)
- `p2pPeers` (int?, opcional)
- `uptimeSeconds` (long?, opcional)
- `processCount` (int?, opcional)

Exemplo:

```json
{
	"agentId": "d2719a7d-43bb-4e7e-bbe6-18dce7bf1db7",
	"clientId": "11111111-1111-1111-1111-111111111111",
	"siteId": "22222222-2222-2222-2222-222222222222",
	"hostname": "HOMOLOG-WIN-01",
	"timestampUtc": "2026-05-05T10:00:00Z",
	"cpuPercent": 17.3,
	"memoryPercent": 42.1,
	"diskPercent": 58.2,
	"processCount": 120
}
```

### 3.2 Command unicast e fan-out

Subjects de comando:

1. Unicast: `tenant.{c}.site.{s}.agent.{a}.command`
2. Broadcast por site: `tenant.{c}.site.{s}.agents.command`
3. Broadcast por cliente: `tenant.{c}.agents.command`
4. Broadcast global: `tenant.global.agents.command`

Envelope recomendado para unicast e fan-out:

```json
{
	"dispatchId": "guid",
	"commandId": "guid|null",
	"commandType": "shell|powershell|script|filetransfer|systeminfo|restart|shutdown|update|remotedebug|showpsadtalert|notification",
	"targetScope": "agent|site|client|global",
	"targetClientId": "guid|null",
	"targetSiteId": "guid|null",
	"issuedAtUtc": "2026-05-05T10:00:00Z",
	"expiresAtUtc": "2026-05-05T10:30:00Z|null",
	"idempotencyKey": "string",
	"payload": "json-string"
}
```

Regras:

1. `dispatchId` e obrigatorio para rastrear campanha de execucao.
2. `idempotencyKey` e obrigatorio para dedupe no agent.
3. Em fan-out, o servidor publica uma unica vez por escopo; o broker replica para os subscribers.
4. `targetScope` deve ser coerente com o subject publicado.

### 3.3 Result (`tenant.{c}.site.{s}.agent.{a}.result`)

```json
{
	"dispatchId": "guid|null",
	"commandId": "guid|null",
	"agentId": "guid",
	"exitCode": 0,
	"output": "string",
	"errorMessage": "string"
}
```

### 3.4 Sync Ping (`tenant.{c}.site.{s}.agent.{a}.sync.ping`)

Campos:

- `eventId` (Guid, obrigatorio)
- `agentId` (Guid, obrigatorio)
- `eventType` (string, obrigatorio; esperado `sync.invalidated`)
- `resource` (string, obrigatorio)
- `scopeType` (string, obrigatorio)
- `scopeId` (Guid?, opcional)
- `installationType` (string?, opcional)
- `revision` (string, obrigatorio)
- `reason` (string?, opcional)
- `changedAtUtc` (DateTime, obrigatorio)
- `correlationId` (string?, opcional)

### 3.5 Remote debug log (`tenant.{c}.site.{s}.agent.{a}.remote-debug.log`)

```json
{
	"sessionId": "guid",
	"agentId": "guid",
	"message": "string",
	"level": "info|debug|warn|error",
	"timestampUtc": "2026-05-05T10:00:00Z",
	"sequence": 1
}
```

### 3.6 P2P discovery (`tenant.{c}.site.{s}.p2p.discovery`)

```json
{
	"version": 1,
	"clientId": "11111111-1111-1111-1111-111111111111",
	"siteId": "22222222-2222-2222-2222-222222222222",
	"generatedAtUtc": "2026-05-05T10:00:00Z",
	"ttlSeconds": 300,
	"sequence": 10,
	"peers": [
		{
			"agentId": "d2719a7d-43bb-4e7e-bbe6-18dce7bf1db7",
			"peerId": "peer-01",
			"addrs": ["10.0.0.10"],
			"port": 4222,
			"lastHeartbeatAtUtc": "2026-05-05T09:59:58Z"
		}
	]
}
```

### 3.7 Dashboard events (`*.dashboard.events`)

Envelope canonico:

```json
{
	"eventType": "AgentHeartbeat|AgentStatusChanged|CommandCompleted|AgentHardwareReported|AgentConnected|AgentDisconnected",
	"data": {},
	"timestampUtc": "2026-05-05T10:00:00Z",
	"clientId": "11111111-1111-1111-1111-111111111111",
	"siteId": "22222222-2222-2222-2222-222222222222"
}
```

Regras:

1. `timestampUtc` obrigatorio em ISO-8601 UTC.
2. `eventType` em PascalCase e enum fechado.
3. `clientId` e `siteId` no envelope raiz (fallbacks somente durante transicao).
4. Em modo estrito, aliases devem ser rejeitados.

### 3.8 Server pong global (`tenant.global.pong`)

```json
{
	"eventType": "pong",
	"serverTimeUtc": "2026-05-05T10:00:00Z",
	"serverOverloaded": "true|false|null"
}
```

Regras:

1. O pong global e sinal de disponibilidade do servidor para todos os agents conectados.
2. Nao deve ser persistido para replay no stream de fan-out de comandos.
3. Agent deve tratar pong apenas como sinalizacao de liveness, sem acao destrutiva.
4. Se `serverOverloaded=true`, agent deve aplicar backoff em trafego nao essencial.
5. Estado padrao atual do servidor: `serverOverloaded` nulo ou omitido.

---

## 4. Regras de roteamento e matching NATS

1. Assinatura em um subject nao inclui subjects mais especificos automaticamente.
2. `tenant.{c}.site.{s}.dashboard.events` NAO recebe `tenant.{c}.site.{s}.agent.{a}.dashboard.events`.
3. O frontend deve consumir somente subjects de dashboard (`*.dashboard.events`).
4. Subjects operacionais nao devem ser expostos ao browser.
5. Fan-out operacional de comando deve usar `*.agents.command` por escopo, evitando loop publish unicast N vezes.
6. Agents devem deduplicar execucao por `dispatchId`/`idempotencyKey` dentro de janela TTL.
7. `tenant.global.pong` e broadcast efemero de liveness do servidor e nao faz parte de replay JetStream.

---

## 5. Projeto de ACL por perfil (NATS WS)

### 5.1 Matriz de permissao

| Perfil | Subscribe allow | Publish allow | Bloqueios obrigatorios |
|---|---|---|---|
| SiteUser | `tenant.{c}.site.{s}.dashboard.events` | nenhum | qualquer wildcard fora do site, subjects operacionais |
| ClientManager | `tenant.{c}.site.*.dashboard.events`, `tenant.{c}.dashboard.events` | nenhum | outros clientes, subjects operacionais |
| GlobalAdmin | `tenant.*.site.*.dashboard.events`, `tenant.*.dashboard.events`, `tenant.unscoped.dashboard.events` | nenhum | publish pelo browser |
| AgentIdentity | `tenant.{c}.site.{s}.agent.{a}.command`, `tenant.{c}.site.{s}.agents.command`, `tenant.{c}.agents.command`, `tenant.global.agents.command`, `tenant.global.pong`, `tenant.{c}.site.{s}.agent.{a}.sync.ping`, `tenant.{c}.site.{s}.p2p.discovery` | `tenant.{c}.site.{s}.agent.{a}.heartbeat`, `tenant.{c}.site.{s}.agent.{a}.result`, `tenant.{c}.site.{s}.agent.{a}.hardware`, `tenant.{c}.site.{s}.agent.{a}.remote-debug.log` | bloquear subscribe/publish fora do escopo do tenant/site/agent |
| ServerCommandPublisher | `tenant.*.site.*.agent.*.result` | `tenant.{c}.site.{s}.agent.{a}.command`, `tenant.{c}.site.{s}.agents.command`, `tenant.{c}.agents.command`, `tenant.global.agents.command` | nao expor credencial no browser |
| BackendInternal | conforme servico | opcional e restrito | nao reutilizar token de backend no browser |

### 5.2 Regras de emissao de JWT

1. JWT de frontend deve conter apenas `sub.allow`.
2. `pub.allow` para browser deve ser vazio.
3. Falha de autorizacao em SUB deve encerrar conexao ou negar inscricao.
4. Claims devem sempre refletir o escopo real do usuario (site/client/global).
5. Token de agent deve incluir subjects de fan-out somente do proprio escopo (`site`, `client`, `global`), sem wildcard amplo multi-tenant.
6. Token do publicador de comando deve ser exclusivo de backend (nao reutilizar em UI).

### 5.3 Exemplo de claim (SiteUser)

```json
{
	"nats": {
		"sub": {
			"allow": [
				"tenant.11111111-1111-1111-1111-111111111111.site.22222222-2222-2222-2222-222222222222.dashboard.events"
			]
		},
		"pub": {
			"allow": []
		}
	}
}
```

### 5.4 Exemplo de claim (GlobalAdmin)

```json
{
	"nats": {
		"sub": {
			"allow": [
				"tenant.*.site.*.dashboard.events",
				"tenant.*.dashboard.events",
				"tenant.unscoped.dashboard.events"
			]
		},
		"pub": {
			"allow": []
		}
	}
}
```

---

## 6. Guia de consumo no frontend

1. Abrir conexao NATS WS autenticada.
2. Assinar subject conforme escopo atual do usuario.
3. Ao trocar escopo, fazer `UNSUB` do subject antigo e `SUB` no novo.
4. Processar somente envelope canonico com normalizacao/validacao.
5. Em payload invalido, logar `console.warn('[CONTRACT_VIOLATION]', detalhes)` e descartar.

## 6.1 Guia de dispatch no servidor (fan-out)

1. Criar `dispatchId` e `idempotencyKey` por campanha.
2. Persistir campanha com escopo (`site`, `client`, `global`) e payload.
3. Publicar uma unica vez no subject de escopo (`*.agents.command`).
4. Receber resultados por agent em `.result` contendo `dispatchId`.
5. Consolidar progresso por campanha (pendente, sucesso, falha, expirado).
6. Reemitir apenas para pendentes em retries controlados.

---

## 7. Checklist de conformidade rapida

### Agent (Go)

- Publica apenas: `heartbeat`, `result`, `hardware`, `remote-debug.log`.
- Assina: `command` unicast + `*.agents.command` por escopo + `tenant.global.pong` + `sync.ping` + `p2p.discovery`.
- Heartbeat com `timestampUtc` e `processCount`.
- Dedupe local por `dispatchId`/`idempotencyKey` para nao executar campanha duplicada.

### Servidor (C#)

- Converte eventos operacionais para `*.dashboard.events`.
- Valida contrato e registra `[CONTRACT_VIOLATION]`.
- Emite ACL por escopo correto no JWT.
- Publica comando em massa via `tenant.{c}.site.{s}.agents.command`, `tenant.{c}.agents.command` e `tenant.global.agents.command`.
- Rastreia campanha por `dispatchId` sem loop unicast para grandes lotes.

### Frontend (TS)

- Consome apenas `*.dashboard.events`.
- Nao assina subjects operacionais.
- Rejeita evento fora do contrato canonico.

---

## 8. Gap identificado para endurecimento

1. Garantir que a emissao de JWT global inclua `tenant.unscoped.dashboard.events` no conjunto de `sub.allow`.
2. Adicionar persistencia de campanha (dispatch) e janela de dedupe por agent.

---

Fim do documento. Versao 1.1.0 - 2026-05-05.