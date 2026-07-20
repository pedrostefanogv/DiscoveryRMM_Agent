# Plano de Revisão, Correção de Bugs e Otimização

> **Data**: 2026-07-19
> **Branch**: `dev`
> **Escopo**: revisão completa do projeto Discovery Agent (Go + Wails + frontend vanilla JS)
> **Status**: Rascunho para execução faseada

---

## 1. Sumário Executivo

A revisão cobriu:
- Build e testes locais (`go vet ./...` e `go test ./...`)
- Análise estática de encoding de arquivos (BOM + mojibake duplo-encoding)
- Leitura dos módulos críticos: `app.go`, `agentconn/runtime.go`, `p2p*.go` (20+ arquivos), `automation/service.go`, `inventory/service.go`, `selfupdate/updater.go`, `database/sqlite.go`, `chat.go`, `notification_center.go`, `consolidation_engine.go`, `command_result_outbox.go`, `remote_debug.go`, `agent_decommission.go`, `tray.go`, `sync.go`
- Leitura do frontend (18 arquivos JS + partials HTML)
- Documentação (`ARCHITECTURE.md`, `AGENTS.md`, `SECURITY.md`, `BRANCHING.md`, `DOCs/*`)

Foram identificados **4 classes de problemas**:

1. **Bugs confirmados por testes falhando** (5 testes em `app`, 3 em `app/inventory`, 2 em `internal/agentconn`, 1 em `internal/ctxutil`)
2. **Mojibake (duplo encoding UTF-8)** em 11 arquivos de produção (Go, PowerShell, NSIS, HTML)
3. **Bugs lógicos latentes** em código sem cobertura de testes
4. **Oportunidades de otimização** (performance, arquitetura, observabilidade)

---

## 2. Bugs Confirmados (testes falhando)

### 2.1 `TestAutoDeriveNATSEndpoints_RemoteHostPrefersWSS` e `TestAutoDeriveNATSEndpoints_WSSExternalSkipsNATS`

**Arquivo**: `src/internal/agentconn/runtime.go` (função `autoDeriveNATSEndpoints`, linhas 727–754)

**Sintoma**: Para host remoto (`tngplacas.com.br`) a função deriva `nats://` quando não deveria; quando `NatsUseWssExternal=true` também deriva `nats://`.

**Raiz**: `autoDeriveNATSEndpoints` sempre deriva ambos os endpoints (NATS nativo + WSS) sem diferenciar host local de remoto, e sem respeitar `NatsUseWssExternal`.

**Correção proposta**:
```go
func autoDeriveNATSEndpoints(cfg *Config) (derivedNATS bool, derivedWSS bool) {
    if cfg == nil { return false, false }
    host := strings.TrimSpace(cfg.NatsServerHost)
    if host == "" { return false, false }

    // WSS sempre derivável (exceto se já existir)
    if cfg.NatsWsServer == "" {
        if wssURL, err := buildExternalNATSWSSURL(host); err == nil {
            cfg.NatsWsServer = wssURL
            derivedWSS = true
        }
    }

    // NATS nativo só para host local/privado E quando NatsUseWssExternal=false
    if cfg.NatsServer == "" && !cfg.NatsUseWssExternal && isLocalOrPrivateHost(host) {
        if nativeURL := deriveNativeNATSServerFromHost(host); nativeURL != "" {
            cfg.NatsServer = nativeURL
            derivedNATS = true
        }
    }
    return derivedNATS, derivedWSS
}

func isLocalOrPrivateHost(host string) bool {
    h := strings.TrimSpace(strings.Trim(host, "[]"))
    if h == "localhost" || strings.HasPrefix(h, "127.") || strings.HasPrefix(h, "192.168.") ||
       strings.HasPrefix(h, "10.") || strings.HasPrefix(h, "172.") {
        return true
    }
    return false
}
```

**Prioridade**: 🔴 Alta — afeta conectividade NATS em produção com hosts remotos.

---

### 2.2 `TestDecodeLibp2pGetHeaderAndPayloadRejectsInvalidRange`

**Arquivo**: `src/app/p2p_libp2p_transport_test.go:61` vs `src/app/p2p_libp2p_transport.go:556`

**Sintoma**: Teste espera mensagem `"range retornado invalido"` (sem acento) mas o código retorna `"range retornado inválido"` (com acento).

**Raiz**: Inconsistência de acentuação entre teste e código.

**Correção**: Alinhar — preferencialmente atualizar o teste para `"range retornado inválido"` (mantendo acentuação correta no código de produção).

**Prioridade**: 🟡 Média — teste quebrado, sem impacto funcional.

---

### 2.3 `TestFindArtifactPeersFromIndex`

**Arquivo**: `src/app/p2p_test.go:133` vs `src/app/p2p_status.go:152` (`FindArtifactPeers`) e `p2p_status.go:70` (`GetPeerArtifactIndex`)

**Sintoma**: Teste configura `peerArtifacts` no coordinator e espera que `FindArtifactPeers` encontre o artifact no cache. Mas `FindArtifactPeers` chama `GetPeerArtifactIndex` que **sempre faz fetch live via libp2p** e ignora o cache `peerArtifacts`.

**Raiz**: Contradição entre o comentário ("Tenta cache primeiro") e a implementação (sempre live). O teste está correto segundo o contrato documentado; o código está divergente.

**Correção proposta**: `FindArtifactPeers` deve consultar `c.peerArtifacts` diretamente (sob `c.mu.RLock()`) antes de chamar `GetPeerArtifactIndex`. Só fazer fetch live em cache miss total.

```go
func (c *p2pCoordinator) FindArtifactPeers(artifactName string) P2PArtifactAvailabilityView {
    safeArtifact := sanitizeArtifactName(artifactName)
    artifactID := CanonicalArtifactID("", safeArtifact, "")
    result := P2PArtifactAvailabilityView{ArtifactID: artifactID, ArtifactName: strings.TrimSpace(safeArtifact), PeerAgentIDs: []string{}}
    if artifactID == "" { return result }

    // 1. Cache primeiro (rápido, sem rede)
    cacheHadEntry := false
    c.mu.RLock()
    for peerKey, state := range c.peerArtifacts {
        for _, artifact := range state.Artifacts {
            if strings.EqualFold(strings.TrimSpace(artifact.ArtifactID), artifactID) {
                result.PeerAgentIDs = append(result.PeerAgentIDs, peerKey)
                cacheHadEntry = true
                break
            }
        }
    }
    c.mu.RUnlock()

    // 2. Cache miss total → fetch live
    if !cacheHadEntry { /* ... código existente de libp2p ... */ }
    return result
}
```

**Prioridade**: 🔴 Alta — bug funcional: lookup de peers por artifact não usa cache, causando latência e falhas quando libp2p não está disponível.

---

### 2.4 `TestInstall_NotificationsSuccessSequence` e `TestInstall_NotificationOnExecutionFailure`

**Arquivo**: `src/app/inventory/service_notifications_test.go:67,134` vs `src/app/inventory/service.go:187,211,225`

**Sintoma**: Teste espera `phase == "instalacao"` (sem acento) mas o código usa `"instalação"` (com acento).

**Raiz**: Inconsistência de acentuação. Provavelmente o teste foi escrito antes e o código foi "corrigido" para acentuação, quebrando o teste.

**Correção**: Alinhar — manter acentuação correta no código (`"instalação"`) e atualizar o teste. **OU** remover acentos de ambas as strings (identifiers em metadata não devem depender de locale).

**Recomendação**: Padronizar sem acento em valores de `metadata["phase"]` (identifiers técnicos não devem ter acento) e atualizar o código para `"instalacao"`.

**Prioridade**: 🟡 Média — teste quebrado.

---

### 2.5 `TestRefreshInventory_SkipsCollectionWhenNotProvisioned`

**Arquivo**: `src/app/inventory/service_provisioning_test.go:67` vs `src/app/inventory/service.go:20`

**Sintoma**: Teste espera erro contendo `"nao estiver provisionado"` (sem acento) mas o código retorna `"inventário indisponível enquanto o agente não estiver provisionado"` (com acento).

**Raiz**: Mesmo problema de acentuação.

**Correção**: Alinhar — preferencialmente atualizar o teste para usar `strings.Contains(err.Error(), "estiver provisionado")` (substring sem acento que existe em ambos).

**Prioridade**: 🟡 Média.

---

### 2.6 `TestBuildPSADTInstallScript_BySource`

**Arquivo**: `src/app/psadt_install_source_test.go:36` vs `src/app/psadt_debug_bridge.go:222`

**Sintoma**: Teste espera `"offline source nao encontrada"` (sem acento) mas o código retorna `"offline source não encontrada"` (com acento).

**Raiz**: Mesmo problema de acentuação.

**Correção**: Alinhar — preferencialmente atualizar o teste.

**Prioridade**: 🟡 Média.

---

### 2.7 `TestClearAllP2PArtifacts` (intermitente)

**Arquivo**: `src/app/p2p_test.go:199`

**Sintoma**: `mkdir C:\WINDOWS\Temp\Discovery: Cannot create a file when that file already exists.`

**Raiz**: Ambiente Windows — o diretório `C:\Windows\Temp\Discovery` já existe (criado por outra execução/SYSTEM). `os.MkdirAll` deveria ser idempotente mas o erro indica que `t.Setenv("WINDIR", root)` + `platform.P2PTempDir()` está derivando para `C:\WINDOWS\Temp\Discovery` em vez do temp do test.

**Correção proposta**: `platform.P2PTempDir()` no Windows deve usar `ProgramData` como base (não `WINDIR`). Verificar `src/internal/platform/` e garantir que o teste isole corretamente o diretório.

**Prioridade**: 🟡 Média — teste flaky.

---

### 2.8 `internal/ctxutil` — `Access is denied`

**Sintoma**: `fork/exec C:\Users\pedro\AppData\Local\Temp\go-build...\ctxutil.test.exe: Access is denied.`

**Raiz**: Antivírus/Defender bloqueando execução de binário temporário de teste. **Não é bug de código** — é ambiente.

**Ação**: Documentar no `testing-notes.md` que `internal/ctxutil` pode falhar por AV. Considerar excluir a pasta de scans ou rodar testes em ambiente limpo.

**Prioridade**: 🟢 Baixa — ambiente.

---

## 3. Mojibake (duplo encoding UTF-8) — 11 arquivos

### 3.1 Arquivos afetados

| Arquivo | BOM | Mojibake em strings? |
|---|---|---|
| `src/app/agent_config.go` | ✅ | ✅ (logs e errors) |
| `src/app/app_test.go` | ✅ | (verificar) |
| `src/app/p2p_libp2p_transport.go` | ✅ | ✅ (comentários + strings) |
| `src/app/p2p_onboarding.go` | ✅ | ✅ (logs e errors) |
| `src/app/remote_debug.go` | ✅ | (verificar) |
| `src/app/remote_debug_test.go` | ✅ | (verificar) |
| `src/internal/agentconn/runtime_nats.go` | ✅ | ✅ (logs e errors) |
| `src/internal/automation/service.go` | ✅ | (verificar) |
| `src/internal/selfupdate/updater.go` | ✅ | (verificar) |
| `src/main.go` | ✅ | (verificar) |
| `src/build/windows/installer/project.nsi` | ✅ | ✅ (comentários + strings NSIS) |
| `src/frontend/partials/views/psadtView.html` | ✅ | ✅ (texto visível UI) |
| `build/scripts/build-bootstrap-installer.ps1` | ✅ | ✅ (strings PowerShell) |
| `build/scripts/build-install-installer.ps1` | ✅ | ✅ (strings PowerShell) |

### 3.2 Impacto

- **Strings de log/error** com mojibake aparecem corrompidas na UI, logs de telemetria e mensagens de erro para o usuário.
- **Comentários** com mojibake não afetam runtime mas prejudicam legibilidade e manutenção.
- **NSIS installer** com mojibake em strings pode exibir texto corrompido durante instalação.
- **PowerShell scripts** com mojibake em mensagens de erro podem falhar parsing ou exibir texto corrompido.
- **HTML** com mojibake exibe texto corrompido na UI.

### 3.3 Correção

Para cada arquivo afetado:
1. Ler bytes brutos
2. Decodificar como Latin-1 (ISO-8859-1) → re-codificar como UTF-8 puro (sem BOM)
3. Validar que strings críticas (`"instalação"`, `"não"`, `"configuração"`, etc.) ficam corretas
4. Rodar `go test` para validar

**Ferramenta sugerida** (PowerShell):
```powershell
function Repair-Mojibake {
    param([string]$Path)
    $bytes = [System.IO.File]::ReadAllBytes($Path)
    # Remove BOM se presente
    if ($bytes.Length -ge 3 -and $bytes[0] -eq 0xEF -and $bytes[1] -eq 0xBB -and $bytes[2] -eq 0xBF) {
        $bytes = $bytes[3..($bytes.Length-1)]
    }
    # Tenta interpretar como Latin-1 e re-codificar como UTF-8
    $latin1 = [System.Text.Encoding]::GetEncoding("ISO-8859-1")
    $text = $latin1.GetString($bytes)
    $utf8Bytes = [System.Text.Encoding]::UTF8.GetBytes($text)
    [System.IO.File]::WriteAllBytes($Path, $utf8Bytes)
}
```

**Prioridade**: 🔴 Alta — afeta experiência do usuário e telemetria.

---

## 4. Bugs Lógicos Latentes (sem cobertura de testes)

### 4.1 `GetPeerArtifactIndex` sempre faz fetch live (ignora cache)

**Arquivo**: `src/app/p2p_status.go:70`

**Problema**: A função documenta "fetch live" mas é chamada por `FindArtifactPeers` que deveria usar cache primeiro. Isso causa:
- Latência em cada chamada de `FindArtifactPeers` (5s timeout por peer)
- Falha quando libp2p não está disponível
- Tráfego de rede desnecessário

**Correção**: Ver item 2.3.

---

### 4.2 `downloadArtifactSwarm` — `BytesRead` calculado incorretamente

**Arquivo**: `src/app/p2p_download.go:104-110`

**Problema**: `BytesRead: int64(completed) * manifest.ChunkSize` usa `ChunkSize` (tamanho nominal) mas o último chunk geralmente é menor. Isso faz a barra de progresso "pular" perto do final.

**Correção**: Usar soma acumulada de `chunk.Size` reais:
```go
onChunkComplete: func(completed, total int) {
    bytesRead := int64(0)
    for i := 0; i < completed && i < len(manifest.Chunks); i++ {
        bytesRead += manifest.Chunks[i].Size
    }
    c.emitTransferProgress(p2pTransferProgress{
        BytesRead: bytesRead,
        TotalBytes: manifest.TotalSize,
        // ...
    })
}
```

**Prioridade**: 🟡 Média — UX de progresso.

---

### 4.3 `p2p_http.go` — `downloadArtifact` não valida tamanho recebido

**Arquivo**: `src/app/p2p_http.go:200-230`

**Problema**: `io.Copy(file, resp.Body)` copia sem validar se `size == access.SizeBytes`. Se o servidor enviar bytes truncados, o arquivo é salvo incompleto mas o checksum falha só depois. Melhor validar tamanho antes de checksum.

**Correção**: Após `io.Copy`, comparar `size` com `access.SizeBytes` (se > 0) e falhar cedo.

**Prioridade**: 🟡 Média.

---

### 4.4 `notification_center.go` — `notificationByKey` cresce indefinidamente

**Arquivo**: `src/app/notification_center.go`

**Problema**: `a.notificationByKey` (mapa de idempotência) é populado mas nunca limpo. Em execução longa (agente roda por semanas), pode crescer indefinidamente consumindo memória.

**Correção**: Adicionar TTL ou limpeza periódica (similar a `pruneProcessedEvents` do syncCoordinator).

**Prioridade**: 🟡 Média — memory leak lento.

---

### 4.5 `remote_debug.go` — sessão ativa não tem timeout de inatividade

**Arquivo**: `src/app/remote_debug.go`

**Problema**: `remoteDebugSession` tem `deadline` (TTL absoluto de 1h) mas se o servidor reabrir a sessão com mesmo ID, a antiga não é limpa. Pode haver leak de publishers/goroutines.

**Correção**: `startSession` deve garantir que `stopSession(sessionID, "replaced")` seja chamado para sessão anterior antes de criar nova.

**Prioridade**: 🟡 Média.

---

### 4.6 `agentconn/runtime.go` — `forceHeartbeatCh` pode bloquear em `default`

**Arquivo**: `src/internal/agentconn/runtime.go:265-280`

**Problema**: `ForceHeartbeat()` usa `select { case r.forceHeartbeatCh <- done: ...; default: ... }`. Se a sessão estiver ativa mas o event loop estiver bloqueado em `nc.Publish` lento, o `default` dispara e retorna "sessao inativa" falso-negativo.

**Correção**: Adicionar um timeout curto no envio (ex.: 2s) em vez de `default` imediato.

**Prioridade**: 🟡 Média.

---

### 4.7 `p2p_libp2p.go` — `libp2pMDNSNotifee.HandlePeerFound` não fecha stream em erro

**Arquivo**: `src/app/p2p_libp2p.go:280-340`

**Problema**: Em vários `return` após erro de decode/validação, o stream `s` não é fechado explicitamente (apenas `defer s.Close()` no final). Como os returns estão dentro do escopo do defer, funciona, mas se houver panic entre o `return` e o defer, vaza.

**Correção**: Garantir que `defer s.Close()` esteja no topo da função (já está) e validar que não há paths que abrem novo stream sem defer.

**Prioridade**: 🟢 Baixa — já coberto por defer.

---

### 4.8 `selfupdate/updater.go` — `extractFileVersion` pode retornar versão errada

**Arquivo**: `src/internal/selfupdate/updater.go`

**Problema**: Comentário menciona que `INFO_FILEVERSION` do NSIS pode divergir de `productVersion`. O código já tem fallback para `serverVersion`, mas se `serverVersion` também estiver errado, loop de reinstalação pode ocorrer.

**Correção**: Adicionar log de warning quando `targetVersion != serverVersion` e `targetVersion != currentVersion` simultaneamente.

**Prioridade**: 🟢 Baixa — já tem mitigação.

---

### 4.9 `automation/service.go` — `deferByTask` não é limpo por agente

**Arquivo**: `src/internal/automation/service.go`

**Problema**: `deferByTask map[string]deferState` é populado mas a limpeza por agente (`loadDeferStateForAgent`) só carrega do DB; não remove entradas órfãs do mapa em memória quando agente muda.

**Correção**: Em `loadPersistedForCurrentAgent`, limpar `deferByTask` antes de recarregar.

**Prioridade**: 🟡 Média.

---

### 4.10 `p2p_cloud_bootstrap.go` — `connectCachedPeers` remove peers em falha sem retry

**Arquivo**: `src/app/p2p_cloud_bootstrap.go:160-190`

**Problema**: Se um peer cacheado falha temporariamente (rede flutuante), ele é removido do cache permanentemente. Não há mecanismo de re-adicionar.

**Correção**: Adicionar contador de falhas; só remover após N falhas consecutivas (ex.: 3).

**Prioridade**: 🟡 Média.

---

## 5. Oportunidades de Otimização

### 5.1 Performance

#### 5.1.1 `GetPeerArtifactIndex` paralelo mas sem limite de concorrência
**Arquivo**: `src/app/p2p_status.go:70`
**Problema**: Faz fetch live de todos os peers em paralelo sem semáforo. Em malhas grandes (50+ peers), pode saturar conexões libp2p.
**Ação**: Adicionar semáforo de concorrência (ex.: 8 workers).

#### 5.1.2 `buildChunkManifest` lê arquivo inteiro em memória para SHA256
**Arquivo**: `src/app/p2p_chunks.go:100-180`
**Problema**: Para arquivos grandes (6GB+), aloca buffer de `chunkSize` (8MB) mas faz `sha256.Sum256(data)` por chunk que copia. Já é streaming mas pode otimizar com `sha256.New()` incremental por chunk.
**Ação**: Já usa `fullHash.Write(data)` — validar que não há alocação extra.

#### 5.1.3 `database/sqlite.go` — `SetMaxOpenConns(1)` serializa todas as queries
**Arquivo**: `src/internal/database/sqlite.go:80`
**Problema**: SQLite com `SetMaxOpenConns(1)` serializa todas as operações. Em cargas com muitas leituras (cache, inventory, automation), vira gargalo.
**Ação**: Usar `SetMaxOpenConns(1)` para escritas mas permitir pool de leituras com `SetMaxIdleConns` maior. Ou usar WAL com `PRAGMA busy_timeout` e permitir mais conexões.

#### 5.1.4 `inventory/osquery_client.go` — `globalOsqueryiPool` singleton global
**Arquivo**: `src/internal/inventory/osquery_client.go:40-100`
**Problema**: Pool global com mutex pode ser ponto de contenção. Em coletas concorrentes (heartbeat + inventory), serializa.
**Ação**: Considerar pool por coleta ou usar `sync.Pool` para sockets.

---

### 5.2 Arquitetura

#### 5.2.1 Arquivos grandes (>400 linhas) candidatos a split
| Arquivo | Linhas | Split sugerido |
|---|---|---|
| `internal/agentconn/runtime.go` | ~900 | `runtime_session.go`, `runtime_nats.go` (já existe), `runtime_tls.go` (já existe) |
| `internal/ai/chat.go` | ~500 | `chat_request.go`, `chat_stream.go`, `chat_tools.go` |
| `internal/automation/service.go` | ~400 | `service_sync.go`, `service_execute.go`, `service_defer.go` |
| `app/p2p.go` | ~500 | `p2p_coordinator.go`, `p2p_lifecycle.go` |
| `app/p2p_libp2p_transport.go` | ~660 | `p2p_libp2p_handlers.go`, `p2p_libp2p_client.go` |
| `app/p2p_chunks.go` | ~400 | `p2p_chunks_manifest.go`, `p2p_chunks_download.go` |
| `internal/database/sqlite.go` | ~600 | `sqlite_cache.go`, `sqlite_automation.go`, `sqlite_inventory.go` |

#### 5.2.2 `app.go` (1200+ linhas) — hub central muito carregado
**Problema**: `App` struct tem 40+ campos e `app.go` mistura lifecycle, config, startup, shutdown, heartbeat, activity tracking.
**Ação**: Extrair `App` em sub-structs: `AppStartup`, `AppHeartbeat`, `AppActivity`, mantendo `App` como compositor.

#### 5.2.3 Frontend sem build system
**Problema**: 18 arquivos JS vanilla carregados via `<script>` sequencial no `bootstrap-partials.js`. Sem minificação, sem tree-shaking, sem source maps.
**Ação**: Avaliar introdução de esbuild/vite para bundle + minify. Ganho de performance de carregamento inicial.

---

### 5.3 Observabilidade

#### 5.3.1 Logs sem nível estruturado
**Problema**: `a.logs.append("[p2p] ...")` é texto livre. Difícil filtrar por nível (debug/info/warn/error).
**Ação**: Introduzir logger estruturado (ex.: `slog` do Go 1.21+) com níveis e campos.

#### 5.3.2 Métricas P2P não expostas via Prometheus
**Problema**: `P2PMetrics` (bytesServed, bytesDownloaded, replicationsSucceeded, etc.) só vão para telemetria server-side.
**Ação**: Expor via endpoint HTTP `/metrics` (Prometheus) para debug local.

#### 5.3.3 `watchdog` não persiste estado entre reinícios
**Problema**: `internal/watchdog` (referenciado na memória mas não encontrado no filesystem — verificar) mantém estado em memória. Em restart, perde histórico de health.
**Ação**: Persistir heartbeats em SQLite.

---

### 5.4 Segurança

#### 5.4.1 `p2p_http.go` — token HMAC sem rotação persistida
**Problema**: `s.secret` é gerado em memória a cada startup. Peers que tinham token válido perdem acesso após restart.
**Ação**: Persistir `secret` em SQLite (com rotação periódica) para sobreviver restarts.
**Status**: ⏸️ **DEFERIDO** — depende de o servidor implementar o compartilhamento de secret entre agents. Por ora, o secret permanece em memória (renovado a cada startup) pois não há mecanismo server-side para distribuir um secret compartilhado entre peers da malha P2P.

#### 5.4.2 `agent_decommission.go` — DELETE sem idempotência explícita
**Problema**: `performAgentDecommissionDelete` retorna nil para 404/410 (já deletado) mas o outbox pode re-tentar 200/204 como sucesso.
**Ação**: Validar que 200/204 também sejam tratados como sucesso (já é, mas documentar).

#### 5.4.3 `runtime_nats.go` — token em log
**Problema**: Logs de heartbeat incluem `authToken` mascarado, mas em alguns paths de erro o token completo pode vazar.
**Ação**: Auditoria de todos os `logf` que incluem `cfg.AuthToken` e garantir mascaramento.

---

### 5.5 Testes

#### 5.5.1 Cobertura de testes baixa em P2P
**Problema**: `p2p_libp2p_transport.go` (660 linhas) tem apenas 2 testes. `p2p_download.go`, `p2p_gossip.go`, `p2p_replication.go` sem testes unitários.
**Ação**: Adicionar testes com mocks de `host.Host` e `network.Stream`.

#### 5.5.2 Sem testes de integração frontend
**Problema**: Frontend (18 arquivos JS) sem testes automatizados.
**Ação**: Avaliar introdução de Playwright/Vitest para testes de UI.

#### 5.5.3 Sem CI/CD visível
**Problema**: Não há `.github/workflows/` no workspace (verificar).
**Ação**: Criar workflow de CI que roda `go vet`, `go test`, `wails build` em push/PR.

---

## 6. Plano de Execução Faseado

### Fase 1 — Correções urgentes (1-2 dias)
1. **Fix mojibake** em 11 arquivos (item 3)
2. **Fix `autoDeriveNATSEndpoints`** (item 2.1)
3. **Fix `FindArtifactPeers` usar cache** (item 2.3)
4. **Alinhar acentuação testes vs código** (itens 2.2, 2.4, 2.5, 2.6)
5. **Fix `TestClearAllP2PArtifacts`** (item 2.7)

### Fase 2 — Bugs lógicos (3-5 dias)
1. **Fix `downloadArtifactSwarm` progresso** (item 4.2)
2. **Fix `p2p_http.go` validação de tamanho** (item 4.3)
3. **Fix `notificationByKey` leak** (item 4.4)
4. **Fix `remote_debug` sessão órfã** (item 4.5)
5. **Fix `forceHeartbeatCh` bloqueio** (item 4.6)
6. **Fix `deferByTask` limpeza** (item 4.9)
7. **Fix `connectCachedPeers` retry** (item 4.10)

### Fase 3 — Otimização de performance (1-2 semanas)
1. **`GetPeerArtifactIndex` semáforo** (item 5.1.1)
2. **SQLite pool de leituras** (item 5.1.3)
3. **`osqueryiPool` por coleta** (item 5.1.4)

### Fase 4 — Arquitetura (2-4 semanas)
1. **Split arquivos grandes** (item 5.2.1)
2. **Extrair sub-structs de `App`** (item 5.2.2)
3. **Avaliar build system frontend** (item 5.2.3)

### Fase 5 — Observabilidade e segurança (1-2 semanas)
1. **Logger estruturado `slog`** (item 5.3.1)
2. **Métricas Prometheus** (item 5.3.2)
3. **Persistir `watchdog` state** (item 5.3.3)
4. **Persistir `p2p_http` secret** (item 5.4.1)
5. **Auditoria de token em logs** (item 5.4.3)

### Fase 6 — Testes e CI (contínuo)
1. **Cobertura P2P** (item 5.5.1)
2. **Testes frontend** (item 5.5.2)
3. **CI/CD workflow** (item 5.5.3)

---

## 7. Métricas de Sucesso

| Métrica | Baseline | Alvo |
|---|---|---|
| Testes falhando | 11 | 0 |
| Arquivos com mojibake | 11 | 0 |
| Arquivos .go com BOM | 13 | 0 |
| Cobertura de testes P2P | ~10% | >60% |
| Latência `FindArtifactPeers` | 5s (live) | <100ms (cache) |
| Linhas máximas por arquivo | 1200 (`app.go`) | <500 |

---

## 8. Riscos e Mitigações

| Risco | Mitigação |
|---|---|
| Correção de mojibake pode introduzir quebra de strings | Validar com `go test` após cada arquivo |
| Split de arquivos pode quebrar imports | Usar `goimports` e `go vet` após cada split |
| Mudança em `autoDeriveNATSEndpoints` pode afetar deployments existentes | Testar com hosts locais e remotos antes de merge |
| Otimização SQLite pode causar lock contention | Testar com carga de produção simulada |

---

## 9. Próximos Passos Imediatos

1. **Criar branch** `bugfix/revisao-2026-07-19` a partir de `dev`
2. **Executar Fase 1** (correções urgentes) — PR único
3. **Validar** com `go test ./...` e `wails build`
4. **Abrir PR** para `dev` com checklist de validação
5. **Agendar Fase 2** após merge da Fase 1

---

## Apêndice A — Comandos de Validação

```powershell
# Build
cd src
wails build -o bin\discovery-agent.exe

# Testes
cd src
go vet ./...
go test ./... -count=1 -timeout 120s

# Verificar mojibake
Get-ChildItem -Path . -Recurse -Include *.go,*.ps1,*.nsi,*.html -File | ForEach-Object {
    $bytes = [System.IO.File]::ReadAllBytes($_.FullName)
    if ($bytes.Length -ge 3 -and $bytes[0] -eq 0xEF -and $bytes[1] -eq 0xBB -and $bytes[2] -eq 0xBF) {
        $utf8 = [System.Text.Encoding]::UTF8.GetString($bytes)
        if ($utf8 -match 'Ã|Ã©|Ã¡|Ã³|Ã­|Ãª|Ã§|â€') {
            Write-Output "MOJIBAKE: $($_.FullName)"
        }
    }
}
```
