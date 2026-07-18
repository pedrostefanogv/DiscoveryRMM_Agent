# Plano de Melhoria — Importação P2P (CPU/Disk Usage)

> **Status**: ✅ TODAS AS FASES CONCLUÍDAS | **Data**: 2026-07-18 | **Branch**: dev

---

## Fase 0.1 — CPU Sampler Instantâneo ✅ CONCLUÍDO
**Bug**: Throttling usa `getHeartbeatMetrics()` que é caro (coleta CPU, memória, disco, processos) e o `collectWindowsCPUPercentNative` usa sliding-window que não funciona em intervalos < 1s.
**Ação**: Criar `CPUSampler` leve com `GetSystemTimes` isolado, sem coletar outras métricas.
**Arquivos**: `src/internal/platform/cpu_sampler_windows.go` (novo), `cpu_sampler_other.go` (stub)
**Integração**: `p2p.go` (struct field), `p2p_publish.go`, `p2p_cache.go`, `p2p_libp2p_transport.go`

---

## Fase 0.2 — Remover ensureManifestForArtifact do ListArtifacts ✅ CONCLUÍDO
**Bug**: `ListArtifacts` é chamado por 6 callers diferentes (frontend, HTTP, replicação, telemetria 1min, automação) e dispara `ensureManifestForArtifact` em goroutine para CADA artifact, causando tempestade de I/O.
**Ação**: Remover `go c.ensureManifestForArtifact(...)` do loop em `ListArtifacts` + remover função obsoleta.
**Arquivos**: `src/app/p2p_publish.go`

---

## Fase 0.3 — Dedup de Geração de Manifest ✅ CONCLUÍDO
**Bug**: `generateManifestEager` pode rodar simultaneamente no mesmo arquivo, causando 2x leituras + 256 SHA256.
**Ação**: Adicionar `manifestInFlight sync.Map` no `p2pCoordinator`; usar `LoadOrStore` para garantir que apenas uma goroutine gere o manifest por artifactName.
**Arquivos**: `src/app/p2p.go`, `src/app/p2p_publish.go`

---

## Fase 0.4 — Validar mtime no Manifest ✅ CONCLUÍDO
**Bug**: `manifestMatchesFile` valida apenas tamanho do arquivo, não mtime. Se arquivo é sobrescrito com mesmo tamanho, manifest cacheado é usado incorretamente.
**Ação**: Adicionar `SourceMTime int64` em `P2PChunkManifest` e validar mtime em `manifestMatchesFile` + popular no `buildChunkManifest`.
**Arquivos**: `src/app/p2p_chunks.go`, `src/app/p2p_cache.go`

---

## Fase 1.1 — Single-pass Copy + Hash + Manifest ✅ CONCLUÍDO
**Problema**: `PublishFile` faz 4 leituras do arquivo (2x SHA256 full + 1x cópia + 1x buildChunkManifest).
**Ação**: Refatorar `PublishFile` e `PublishFileWithIDAndVersion` para usar `publishFileSinglePass()` que lê o arquivo UMA vez com `io.MultiWriter` + `sha256.New()` streaming + `sha256.Sum256` por chunk.
**Arquivos**: `src/app/p2p_publish.go`

---

## Fase 1.2 — Buffer de I/O 4 MB ✅ CONCLUÍDO
**Problema**: `io.Copy` usa buffer 32 KB padrão, gerando ~32K syscalls para 1 GB.
**Ação**: `publishFileSinglePass` usa buffer de 4 MB (`make([]byte, 4<<20)`), reduzindo syscalls para ~256.
**Arquivos**: `src/app/p2p_publish.go` (embutido no single-pass)

---

## Fase 2.1 — Rate Limiter de Disco ✅ CONCLUÍDO
**Problema**: Leitura sequencial satura o disco, causando latência para outros apps.
**Ação**: Rate limiter inline no `publishFileSinglePass` — a cada 16 chunks (~128 MB) verifica bytes/s; se > 100 MB/s, dorme o necessário.
**Arquivos**: `src/app/p2p_publish.go` (constante `p2pImportMaxBytesPerSec` + lógica no loop)

---

## Fase 2.2 — I/O Priority Windows ✅ CONCLUÍDO
**Problema**: Thread de import compete em igualdade com processos foreground.
**Ação**: Criar `platform.OpenFileSequential()` usando `FILE_FLAG_SEQUENTIAL_SCAN` (0x08000000) via `syscall.CreateFile`. Integrar no `publishFileSinglePass`.
**Arquivos**: `src/internal/platform/io_priority_windows.go` (novo), `io_priority_other.go` (stub), `src/app/p2p_publish.go`

---

## Fase 3.1 — Progress Feedback para UI ✅ CONCLUÍDO
**Problema**: Importação é opaca; usuário não vê progresso.
**Ação**: Emitir evento Wails `p2p:publish:progress` com % concluído a cada 16 chunks durante `publishFileSinglePass` + evento final com `Done: true`.
**Arquivos**: `src/app/p2p_publish.go` (struct `p2pPublishProgress`, método `emitPublishProgress`, emissão no loop e no erro), `src/frontend/js/app-p2p.js` (listener `onP2PPublishProgress` integrado ao painel de transferências)

---

## Fase 3.2 — Manifest Lazy (sob demanda) ✅ CONCLUÍDO
**Problema**: `generateManifestEager` sempre gera manifest mesmo se artifact nunca for baixado.
**Ação**: `PublishFile` e `PublishFileWithIDAndVersion` agora geram manifest inline via `publishFileSinglePass` + `cacheManifestAfterSinglePass`. `PublishTestArtifact` não chama mais `generateManifestEager` — o manifest será gerado sob demanda no primeiro download.
**Arquivos**: `src/app/p2p_publish.go` (removido `go c.generateManifestEager` do `PublishTestArtifact`)

---

## 📊 Resumo de Mudanças

| Arquivo | Status | Mudança |
|---------|--------|---------|
| `src/app/p2p_publish.go` | Modificado | Single-pass, rate limiter, dedup, remoção ensureManifestForArtifact, cpuSampler |
| `src/app/p2p_chunks.go` | Modificado | SourceMTime no P2PChunkManifest |
| `src/app/p2p_cache.go` | Modificado | manifestMatchesFile valida mtime, cpuSampler |
| `src/app/p2p.go` | Modificado | manifestInFlight, cpuSampler field |
| `src/app/p2p_libp2p_transport.go` | Modificado | cpuSampler |
| `src/internal/platform/cpu_sampler_windows.go` | NOVO | CPUSampler leve |
| `src/internal/platform/cpu_sampler_other.go` | NOVO | Stub não-Windows |
| `src/internal/platform/io_priority_windows.go` | NOVO | FILE_FLAG_SEQUENTIAL_SCAN |
| `src/internal/platform/io_priority_other.go` | NOVO | Stub não-Windows |
| `src/frontend/js/app-p2p.js` | Modificado | Listener `onP2PPublishProgress` + evento `p2p:publish:progress` |

---

## 🔧 Hotfix Adicional: Chunks P2P Falhando com "leitura incompleta" (2026-07-18)

**Sintoma**: Chunks próximos ao final do download falham com `leitura incompleta: esperado=8388608 recebido=XXXXXXX: connection closed (remote)`.

**Causa raiz**: O artifact `.importing` (em cópia) era listado pelo `ListArtifacts`, anunciado via gossip e peers tentavam baixar antes do arquivo estar completo.

**Correções**:
1. `ListArtifacts` — ignorar arquivos com sufixo `.importing`
2. `handleStreamArtifactGet` — rejeitar requests para `.importing` com erro explícito
3. `handleStreamArtifactGet` — `s.SetDeadline(computeTransferDeadline(chunkLen))` antes de `io.Copy` para evitar timeout libp2p no servidor

**Arquivos**: `p2p_publish.go`, `p2p_libp2p_transport.go`

---

## 🔧 Hotfix: os.Rename "arquivo em uso" — RenameAtomic com retry (2026-07-18)

**Sintoma**: Falha ao renomear `.importing`/`.partial` → arquivo final com erro "being used by another process".

**Causa raiz**: No Windows, antivírus, indexador de busca ou outros processos podem abrir o arquivo brevemente entre `Close()` e `Rename()`. Sem retry, o `os.Rename` falha definitivamente.

**Correções**:
1. **`platform.RenameAtomic(old, new)`** — 5 tentativas com backoff exponencial (100ms, 200ms, 400ms, 800ms, 1600ms); fallback `copy + delete` se todas falharem
2. **`dstFile.Sync()` antes do `Close()` no `publishFileSinglePass`** — garante que todos os bytes estão flushados antes de tentar renomear, evitando que AV/intermediários vejam arquivo parcial
3. Substituição de `os.Rename` por `platform.RenameAtomic` nos 4 pontos P2P: `publishFileSinglePass`, `downloadChunkedLibp2p`, `downloadViaHTTP` e `libp2pDownloadArtifact`

**Arquivos**: `internal/platform/rename.go` (novo), `p2p_publish.go`, `p2p_chunks.go`, `p2p_http.go`, `p2p_libp2p_transport.go`

---

## 🔧 Hotfix: Progresso de download oscilante + Hostname nos peers (2026-07-18)

### Problema 1 — Barra de progresso "indo e voltando"

**Causa**: O callback `onChunkProgress` reportava `readSoFar` DENTRO de cada chunk individual (0→8MB), e com 4 chunks em paralelo chunks diferentes reportavam valores conflitantes, fazendo a barra subir e descer.

**Correções**:
1. **Backend**: Adicionado `onChunkComplete(completed, total)` chamado quando cada chunk é salvo com sucesso. O `onChunkProgress` foi desabilitado (passado como `nil`).
2. **Backend**: Adicionado campo `CompletedChunks` no `p2pTransferProgress` — representa chunks já persistidos em disco (monotônico).
3. **Frontend**: `onP2PTransferProgress` recalcula `bytesRead` a partir de `completedChunks × chunkSize`, e garante que nunca diminui (guarda `_monotonicBytes`).

**Arquivos**: `p2p_chunks.go`, `p2p_download.go`, `p2p_transfer_progress.go`, `app-p2p.js`

### Problema 2 — IP aparecendo como nome do agente

**Causa**: O campo `Host` não estava sendo propagado entre peers — `p2pHealthResponse`, `p2pLibP2PPeerInfo` e `p2pDiscoveredPeer` não incluíam hostname.

**Correções**:
1. **`p2pHealthResponse`**: Adicionado campo `Host` (preenchido com `os.Hostname()` no `buildHealthResponse`)
2. **`p2pLibP2PPeerInfo`**: Adicionado campo `Host` (preenchido com `self.Host` no handshake)
3. **LAN probe e libp2p handshake**: Usam `health.Host` / `remote.Host` com fallback para IP
4. **Frontend**: `displayName` agora prioriza `peer.host > peer.clientId > peer.agentId`

**Arquivos**: `p2p_lan_probe.go`, `p2p_libp2p.go`, `p2p_discovery.go`, `p2p_status.go`, `app-p2p.js`

