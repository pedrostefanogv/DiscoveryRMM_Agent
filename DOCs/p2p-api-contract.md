# P2P API Contract — Discovery Agent

This document describes all HTTP endpoints exposed by the P2P transfer server
(running locally on each agent) **and** the server-side REST endpoints that the
agent calls for fleet coordination. It is intended as the authoritative reference
for backend developers implementing the server side.

---

## Table of Contents

1. [Local Agent P2P HTTP Server](#1-local-agent-p2p-http-server)
   - [Authentication model](#authentication-model)
   - [Endpoints](#local-endpoints)
2. [Server API (agent → server)](#2-server-api-agent--server)
   - [Authentication](#server-authentication)
   - [GET  /api/agent-auth/me/p2p-seed-plan](#get-apagent-authmeseedd-plan)
   - [POST /api/agent-auth/me/p2p-telemetry](#post-apagent-authmep2p-telemetry)
   - [GET  /api/agent-auth/me/p2p-distribution-status](#get-apagent-authmep2p-distribution-status)
3. [Type Definitions](#3-type-definitions)
4. [Error Handling](#4-error-handling)
5. [Modes & Feature Flags](#5-modes--feature-flags)

---

## 1. Local Agent P2P HTTP Server

Each agent starts an HTTP server on a random port within the configured range
(default `41080–41120`). Peers discover the address via mDNS, UDP broadcast,
or libp2p, then use this server exclusively for pull-based artifact transfers.

### Authentication model

All artifact endpoints require a **short-lived signed token** issued by the
serving agent. The token encodes: artifact name, allowed peer ID, and expiry
timestamp (HMAC-SHA256 over a per-session secret).

Control/replication requests additionally carry an HMAC-SHA256 signature over
`sourceAgentId|artifactName|sha256|timestamp` using a **shared secret**
configured in `P2PConfig.SharedSecret`.

---

### Local Endpoints

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

## 2. Server API (agent → server)

The agent periodically calls these endpoints on the central management server.
All requests require a **Bearer token** in the `Authorization` header.

### Server Authentication

```
Authorization: Bearer <authToken>
```

`authToken` is the per-agent JWT/opaque token stored in `debugConfig.AuthToken`.

---

### GET /api/agent-auth/me/p2p-seed-plan

Fetches a fleet-wide seed plan recommendation for this agent's site.

The agent caches the response for up to 5 minutes before re-fetching.

**Response `200 OK`:**
```json
{
  "siteId":          "site-uuid",
  "generatedAtUtc":  "2026-03-23T12:00:00Z",
  "plan": {
    "totalAgents":       50,
    "configuredPercent": 10,
    "minSeeds":          2,
    "selectedSeeds":     5
  }
}
```

| Field | Type | Notes |
|-------|------|-------|
| `siteId` | string | May be empty for single-site deployments |
| `generatedAtUtc` | RFC3339 string | Server generation timestamp |
| `plan.totalAgents` | int | Total online agents at the site |
| `plan.configuredPercent` | int | Target seeder percentage (0–100) |
| `plan.minSeeds` | int | Minimum seeders regardless of percentage |
| `plan.selectedSeeds` | int | Resolved seeder count to use |

**Server validation rules:**
- `plan.selectedSeeds` must be `≥ plan.minSeeds`
- `plan.selectedSeeds` must be `≤ plan.totalAgents`
- If omitted or zero, the agent falls back to locally-computed values

**Status codes:**
| Code | Reason |
|------|--------|
| `200` | Plan returned |
| `401` | Invalid / missing auth token |
| `404` | Agent unknown |
| `500` | Server error |

---

### POST /api/agent-auth/me/p2p-telemetry

Periodic swarm health telemetry sent by the agent every **5 minutes**.

**Request body:**
```json
{
  "agentId":        "agent-abc123",
  "siteId":         "site-uuid",
  "collectedAtUtc": "2026-03-23T12:00:00Z",
  "metrics": {
    "publishedArtifacts":    3,
    "replicationsStarted":  12,
    "replicationsSucceeded":11,
    "replicationsFailed":    1,
    "bytesServed":     157286400,
    "bytesDownloaded": 52428800,
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

**Response `200 OK` / `204 No Content`:** Accepted (body may be empty).

**Status codes:**
| Code | Reason |
|------|--------|
| `200/204` | Accepted |
| `400` | Malformed payload |
| `401` | Invalid auth |
| `500` | Server error |

---

### GET /api/agent-auth/me/p2p-distribution-status

Returns the distribution visibility for all artifacts tracked for this agent's
fleet. Used by operations dashboards.

**Response `200 OK`:**
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

| Field | Type | Notes |
|-------|------|-------|
| `artifactId` | string | Canonical artifact ID (see §3) |
| `artifactName` | string | Human-readable filename |
| `peerCount` | int | Number of agents currently caching this artifact |
| `peerAgentIds` | string[] | Optional; may be omitted to reduce payload size |
| `lastUpdatedUtc` | RFC3339 string | Last time the server updated its count |

**Status codes:**
| Code | Reason |
|------|--------|
| `200` | List returned (may be empty array) |
| `401` | Invalid auth |
| `500` | Server error |

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

All error responses use plain `text/plain` bodies (not JSON) for consistency
with `http.Error`. Status codes follow standard HTTP semantics.

The agent performs **no automatic retries** on server API calls; the telemetry
loop merely logs failures and waits for the next tick. Temporary server
unavailability is therefore non-disruptive.

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
