# P2P API Contract — Discovery Agent

> Status de consolidacao: ativo.

> Escopo canonico deste documento: contrato e comportamento do agente (transporte local + consumo de APIs do servidor).
>
> Para implementacao detalhada no backend do servidor, usar: DOCs/P2P_SERVER_API_IMPLEMENTATION.md

This document describes the local P2P transport implemented in the agent
(HTTP minimal + libp2p streams) **and** the server-side REST endpoints that the
agent calls for fleet coordination. It is intended as the authoritative reference
for backend developers implementing or integrating with the current transport.

> Update (2026-03): artifact transfer and peer gossip are now primarily handled
> by libp2p stream protocols. Local HTTP is kept for health and onboarding.
> Legacy HTTP transfer endpoints remain documented below for compatibility
> context and migration, but they are not the primary path in the current build.

---

## Table of Contents

1. [Local Agent Transport (HTTP + libp2p)](#1-local-agent-transport-http--libp2p)
  - [Current transport map](#current-transport-map)
   - [Authentication model](#authentication-model)
  - [HTTP local endpoints](#local-endpoints)
  - [libp2p stream protocols (current)](#libp2p-stream-protocols-current)
2. [Server API (agent → server) — resumo](#2-server-api-agent--server--resumo)
3. [Type Definitions](#3-type-definitions)
4. [Error Handling](#4-error-handling)
5. [Modes & Feature Flags](#5-modes--feature-flags)

---

## 1. Local Agent Transport (HTTP + libp2p)

Each agent starts an HTTP server on a random port within the configured range
(default `41080–41120`) and a libp2p host for peer discovery/transfer.

### Current transport map

| Capability | Current path | Notes |
|---|---|---|
| Local health | `GET /p2p/health` | HTTP local endpoint |
| Onboarding pull/push | `GET/PUT /p2p/config/onboard` | HTTP local endpoint |
| Peer gossip | `/discovery/peers/1.0.0` | libp2p stream protocol |
| Artifact access token | `/artifact/access/1.0.0` | libp2p stream protocol |
| Artifact manifest | `/artifact/manifest/1.0.0` | libp2p stream protocol |
| Artifact bytes/chunks | `/artifact/get/1.0.0` | libp2p stream protocol |
| Push replication control | `/artifact/replicate/1.0.0` | Returns gone; pull-only mode |

### Authentication model

All artifact endpoints require a **short-lived signed token** issued by the
serving agent. The token encodes: artifact name, allowed peer ID, and expiry
timestamp (HMAC-SHA256 over a per-session secret).

Control/replication requests additionally carry an HMAC-SHA256 signature over
`sourceAgentId|artifactName|sha256|timestamp` using a **shared secret**
configured in `P2PConfig.SharedSecret`.

---

### Local Endpoints

> Compatibility note: the HTTP endpoints below are kept as reference for
> migration and legacy integrations. In the current implementation, peer gossip
> and artifact transfer should use the libp2p stream protocols documented in
> the next subsection.

#### `GET /p2p/health`

Returns basic liveness info.

**Auth:** None required.

**Response `200 OK`:**
```json
{
  "ok": true,
  "agentId": "agent-abc123"
}
```

---

#### `GET /p2p/peers`

Returns the announcing agent's known peer list and local artifact index.  
**Blocked (410 Gone) in `libp2p_only` mode** — gossip is handled by libp2p.

**Auth:** None required.

**Response `200 OK`:**
```json
{
  "agentId": "agent-abc123",
  "knownPeers": [
    {
      "agentId": "agent-xyz",
      "host": "192.168.1.10",
      "address": "192.168.1.10",
      "port": 41082,
      "source": "mdns",
      "lastSeenUtc": "2026-03-23T12:00:00Z",
      "knownPeers": 3,
      "connectedVia": "mdns"
    }
  ],
  "artifacts": [ /* P2PArtifactView[] — see §3 */ ],
  "catalogSource": "self",
  "updatedAtUtc": "2026-03-23T12:00:00Z"
}
```

**Status codes:**
| Code | Reason |
|------|--------|
| `200` | Success |
| `405` | Non-GET method |
| `410` | libp2p_only mode — gossip disabled |

---

#### `POST /p2p/artifact/access`

Issues a time-limited download token for a named artifact.

**Auth:** None (token issuance is self-authorising; the caller must present the
token on the subsequent download request).

**Request body:**
```json
{
  "artifactName": "MyApp-1.2.3.exe",
  "requesterId": "agent-xyz"
}
```

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `artifactName` | string | ✓ | Sanitised; path traversal sequences rejected |
| `requesterId` | string | — | Defaults to `"peer-anon"` when omitted |

**Response `200 OK`:**
```json
{
  "artifactId":     "name:myapp-1.2.3.exe",
  "artifactName":   "MyApp-1.2.3.exe",
  "url":            "http://192.168.1.5:41080/p2p/artifact/MyApp-1.2.3.exe?peer=agent-xyz&token=BASE64.SIG",
  "checksumSha256": "a1b2c3...",
  "sizeBytes":      52428800,
  "expiresAtUtc":   "2026-03-23T12:15:00Z"
}
```

**Status codes:**
| Code | Reason |
|------|--------|
| `200` | Token issued |
| `400` | Missing / invalid `artifactName` |
| `404` | Artifact not in local temp cache |
| `405` | Non-POST method |
| `500` | Token signing failure |

---

#### `GET /p2p/artifact/{name}?peer=<id>&token=<tok>`

Downloads the artifact file. Supports HTTP Range requests (used by chunked
swarm downloads). The response includes `X-Artifact-SHA256` for integrity
verification.

**Auth:** `peer` + `token` query parameters (HMAC token from `/p2p/artifact/access`).

**Path:** `{name}` = URL-path-encoded artifact filename (no path traversal).

**Response `200 OK` / `206 Partial Content`:**
- Binary content stream  
- Header `X-Artifact-SHA256: <hex>`  
- Header `Content-Length: <bytes>`

**Status codes:**
| Code | Reason |
|------|--------|
| `200/206` | File content |
| `400` | Malformed name or bad range |
| `401` | Missing / expired / invalid token |
| `404` | Artifact not found |
| `405` | Non-GET method |

---

#### `GET /p2p/artifact/{name}/manifest?peer=<id>&token=<tok>`

Returns a chunk manifest describing how the artifact is split for parallel
swarm download. Uses the same token auth as the plain artifact endpoint.

**Auth:** Same `peer` + `token` as artifact download.

**Response `200 OK`:**
```json
{
  "artifactId":   "name:myapp-1.2.3.exe",
  "artifactName": "MyApp-1.2.3.exe",
  "totalSize":    52428800,
  "chunkSize":    8388608,
  "totalChunks":  7,
  "sha256":       "full-file-sha256-hex",
  "chunks": [
    { "index": 0, "offset": 0,       "size": 8388608, "sha256": "chunk0-sha256-hex" },
    { "index": 1, "offset": 8388608, "size": 8388608, "sha256": "chunk1-sha256-hex" }
  ]
}
```

**Status codes:**
| Code | Reason |
|------|--------|
| `200` | Manifest |
| `400` | Malformed name |
| `401` | Invalid token |
| `404` | Artifact not found |
| `405` | Non-GET method |
| `500` | Checksum computation error |

---

#### `GET /p2p/config/onboard`

Returns a signed onboarding offer (serverUrl + deploy key) for unconfigured
peer agents to pull configuration from this agent.

**Auth:** None (the offer itself is HMAC-signed by the issuing agent).

**Response `200 OK`:**
```json
{
  "serverUrl":    "https://server.example.com",
  "deployKey":    "DEPLOY_KEY_VALUE",
  "expiresAtUtc": "2026-03-23T12:30:00Z",
  "sourceAgent":  "agent-abc123",
  "nonce":        "random-hex",
  "signature":    "hmac-base64url"
}
```

**Response `204 No Content`:** Agent is not yet configured (has no server URL /
deploy key to share).

---

#### `PUT /p2p/config/onboard`

Receives a signed onboarding offer pushed from another peer and applies it to
the local configuration.

**Auth:** HMAC signature in the request body (derived from deploy key).

**Request body:** Same structure as the GET response above.

**Response `200 OK`:**
```json
{
  "agentId":    "agent-newly-configured",
  "registered": true,
  "message":    "configuração aplicada"
}
```

**Status codes:**
| Code | Reason |
|------|--------|
| `200` | Successfully applied |
| `400` | Invalid / expired payload |
| `401` | Signature verification failed |
| `405` | Non-PUT/GET method |
| `409` | Agent already configured |
| `503` | Internal error applying config |

---

### libp2p stream protocols (current)

The following protocols are registered in the agent libp2p host and are the
primary data-plane for gossip and artifact transfer:

1. `/discovery/peers/1.0.0`
2. `/artifact/access/1.0.0`
3. `/artifact/manifest/1.0.0`
4. `/artifact/get/1.0.0`
5. `/artifact/replicate/1.0.0`

Request/response payloads map directly to the current transport layer in:

- `app/p2p_libp2p_transport.go`
- `app/p2p_libp2p.go`

`/artifact/replicate/1.0.0` returns a "gone/pull-only" response by design,
as forced push replication is disabled in the current mode.

---

## 2. Server API (agent → server) — resumo

This document keeps only the **agent-consumption summary** of server routes to
avoid duplication with the normative backend guide.

Canonical backend implementation, validation rules, DB model, retention and
rate limiting are maintained in:

- `DOCs/P2P_SERVER_API_IMPLEMENTATION.md`

Agent-facing routes used by current runtime:

1. `GET /api/agent-auth/me/p2p-seed-plan`
2. `POST /api/agent-auth/me/p2p-telemetry`
3. `GET /api/agent-auth/me/p2p-distribution-status`

Current query options used by the agent for distribution status:

- `artifactId` (optional)
- `limit` (optional, 1-500)
- `offset` (optional, >=0)

Auth model used by agent requests:

```text
Authorization: Bearer <authToken>
```

Where this is implemented in code:

- `app/p2p_api.go`
- `app/p2p_telemetry_outbox.go`

---

## 3. Type Definitions

### P2PArtifactView
```json
{
  "artifactId":       "name:myapp.exe",
  "artifactName":     "MyApp.exe",
  "version":          "1.2.3",
  "sizeBytes":        52428800,
  "modifiedAtUtc":    "2026-03-23T10:00:00Z",
  "checksumSha256":   "abc123...",
  "available":        true,
  "lastHeartbeatUtc": "2026-03-23T12:00:00Z"
}
```

### Canonical ArtifactID resolution

The agent resolves `artifactId` using this priority order:

1. **Explicit ID** — if `artifactId` is a non-empty string, use it as-is.
2. **URL fingerprint** — if the artifact has a known source URL, the ID is  
   `urlsha256:<sha256-of-lowercase-url>`.
3. **Name fallback** *(deprecated)* — `name:<lowercase-sanitised-filename>`.

> **Migration note:** Server responses for distribution status and seed plans
> should always include an explicit `artifactId` so the agent never falls back
> to the name-based path. The name fallback logs a `[p2p][deprecated]` warning
> and will be removed in a future version.

---

## 4. Error Handling

For server API calls (`/api/agent-auth/me/p2p-*`), the agent handles
non-2xx responses as errors and attempts to parse JSON error envelopes in the
shape below when present:

```json
{
  "error": "human readable message",
  "field": "optionalField",
  "code": "OPTIONAL_MACHINE_CODE",
  "retryAfterSeconds": 60
}
```

If JSON parsing fails, the agent falls back to plain text body parsing.

Telemetry delivery behavior (current):

1. The initial `POST /api/agent-auth/me/p2p-telemetry` attempt is synchronous.
2. On transport/HTTP failure, payloads are queued in a local SQLite outbox.
3. The periodic telemetry loop drains queued payloads with retry/backoff.
4. On `429 Too Many Requests`, the agent honors `Retry-After` (header and/or
   `retryAfterSeconds` in JSON) and temporarily suppresses new send attempts.
5. `202 Accepted` is treated as success (recommended async processing path).

This yields an at-least-once delivery profile for telemetry, with best-effort
deduplication via idempotency key and payload hash in the outbox path.

---

## 5. Modes & Feature Flags

| P2PMode | Discovery | `/p2p/peers` gossip | Bootstrap peers |
|---------|-----------|---------------------|-----------------|
| `legacy` *(default)* | mDNS/UDP | ✓ Enabled | N/A |
| `hybrid` | mDNS + libp2p | ✓ Enabled | Optional |
| `libp2p_only` | libp2p only | ✗ **Blocked (410)** | Optional |

### Bandwidth limiting (`MaxBandwidthBytesPerSec`)

When set to a non-zero value, the chunk scheduler enforces a token-bucket
rate limiter shared across all parallel chunk downloads. The burst window is
4 MB, allowing short bursts without degrading average-rate control.
Minimum enforced value: **64 KB/s** (values below this floor are clamped up).

### Bootstrap peers (`BootstrapConfig.BootstrapPeers`)

A list of libp2p multiaddr strings (must include peer IDs) to connect to at
startup. Enables peer discovery in environments where mDNS multicast is blocked
(e.g. VPN, corporate VLAN segmentation).

**Format:** `/ip4/<host>/tcp/<port>/p2p/<peerID>`  
**Example:** `/ip4/10.10.0.5/tcp/4001/p2p/12D3KooWAbcDef1234...`
