# PLANO DE CORREÇÕES — Relatório de Análise de Código (Discovery Agent)

> **Data:** 2026-09-10 · **Branch:** `dev` (base `2d28aa0`, v1.2.1)
> **Referência:** `RELATORIO_ANALISE_CODIGO.md` (2026-02-10)
> **Decisão do dono do projeto:** corrigir imediatamente C1, C2, C7, A1, A3, A5, A6, A7, A9, A12, A13, A14, A15, A16, A17; avaliar/planejar C6 e A2; manter por decisão C3, C4, C5, A4, A8, A10, A11 (postergados ou intencionais).

---

## 1. Correções implementadas nesta rodada

| Item | Correção | Arquivos | Verificação |
|---|---|---|---|
| **C1** — download HTTP do self-update sem verificação de SHA-256 | Hash do servidor agora é **obrigatório**: sem hash de referência nenhum download inicia (P2P nem HTTP); após `downloadFromURL` o hash é comparado via `verifyInstallerSHA256` e divergência/ausência **aborta com remoção do arquivo**; no caminho `checkAndUpdateFallback` a divergência virou **erro fatal** (era apenas ALERTA) | `src/app/core/selfupdate/updater_network.go`, `updater.go` | `go test ./app/core/selfupdate/` (testes novos: `updater_network_test.go`) |
| **C2** — P2P aceita binário de qualquer peer sem hash | `downloadFromCacheOrPublic` recusa P2P quando `expectedSHA256 == ""` (tenta `/sha256` primeiro; indisponível → erro sem iniciar download). Verificação do peer é **incondicional**. `InstallFromURL` com endpoint público também exige hash | idem | `go test` (`TestDownloadFromCacheOrPublic_RefusesDownloadWithoutServerHash`) |
| **A9** — panic `serverSHA256[:12]` mata o loop de self-update | Helper `shortHash()` com guarda de tamanho usado em **todas** as fatias `[:12]` do pacote | `updater_network.go`, `updater.go` | `TestShortHash` |
| **C7** — `Everyone:(F)` no staging executado como SYSTEM (TOCTOU→LPE) | `EnsureWorldAccess` → **`EnsureSharedStagingAccess`**: SYSTEM/Administrators com Full Control (via `/grant`) e **Everyone com Read+Execute** (`/grant:r`, substitui grant legado `Everyone:(F)` em instalações existentes). SIDs bem-conhecidos (independente de idioma). Re-hash pré-execução dispensado nesta fase: a DACL fecha a escrita por usuários comuns (atacante do TOCTOU) | `src/app/core/platform/perms_windows.go`, `perms_other.go`, `app/p2p_cleanup.go`, `app/p2p/p2p.go` | `go vet` + testes `app/`, `app/p2p/` |
| **A1** — dedup de notificação retorna `"approved"` implícito (bypass de `require_confirmation`) | `idempotencyEntry` guarda o **resultado final** da notificação original (`Result` + canal `done`); retry com a mesma key aguarda (limitado por `maxDedupResultWait=90s`) e reflete o resultado real; enquanto pendente responde `"pending"` (`Accepted=false`); `approved` só se a original foi aprovada. Registros em todos os pontos de conclusão do `Dispatch` | `src/app/services/notifications/service.go`, `service_test.go` (novos) | 3 testes de regressão passando |
| **A15** — data race fatal em `s.deferByTask` | Leitura em `executeTaskAsync` (linha da notificação final) passa a ser feita **sob `s.mu.Lock`** (era leitura direta fora do lock; `loadDeferStateForAgent` substitui o mapa sob lock → fatal error do runtime) | `src/app/core/automation/service.go` | `go test ./app/core/automation/` |
| **A16** — MCP server: panic "send on closed channel" + leak do semáforo | `Run` agora: (a) `cancelRun()` imediatamente após o loop de leitura; (b) `WaitGroup` de goroutines de request — `respCh` só é fechado **depois** de `wg.Wait()` (nenhum sender restante, elimina o panic por completo, inclusive a corrida do `select`); (c) semáforo liberado **somente** quando a vaga foi adquirida. Rodado com `-race` | `src/app/core/mcp/server.go` | `go test -race ./app/core/mcp/` |
| **A17** — watcher WTS nunca dispara | Registro com `NOTIFY_FOR_ALL_SESSIONS` (1) em vez de `NOTIFY_FOR_THIS_SESSION` (0); filtro da message loop com `WM_WTSSESSION_CHANGE` (`0x02B1`) em vez de `WM_USER+1`; constantes nomeadas | `src/app/core/automation/userlogin_watcher_windows.go` | vet + testes do pacote |
| **A14** — injeção de comando via campo `shell` (WSL distro sem validação) | Validação em camadas: (1) `terminal.ValidateSessionShellKind` na borda (`remotesession/manager.go`) — só ShellKind conhecidos e `wsl:<distro>` com distro validada; (2) `IsValidWSLDistro` (charset `[A-Za-z0-9 ._-]`, sem `--` nem `" -"`, ≤64); (3) em `resolveShellCommand`, quando possível aceita apenas distros **instaladas** (`IsWSLAvailable`); (4) `buildCommandLine` faz quoting de args com espaço | `src/app/core/terminal/shell_validate.go` (novo), `pty_conpty_shell.go`, `shell_validate_test.go` (novo), `core/remotesession/manager.go` | `go test ./app/core/terminal/` |
| **A6** — path traversal via `manifest.ArtifactName` remoto | `downloadChunkedLibp2p` sanitiza o nome remoto (`SanitizeArtifactName`), **rejeita manifest com nome vazio** e rejeita quando diverge do nome solicitado; o manifest sanitizado é usado nos joins de `partsDir`/`targetPath` | `src/app/p2p/p2p_chunks.go` | `go test -race ./app/p2p/` |
| **A7** — data race em `ArtifactFetchState` | Encapsulamento: novos métodos `fetchStateMap.mutate()` (mutação sob lock, com criação) e `snapshot()` (cópia sob lock). Todos os sites de mutação/leitura reescritos: `handleFetchCandidacy`, `runLocalElection`, `executeFetch`, heartbeat libp2p recebido, reseed | `src/app/p2p/p2p_fetch.go`, `p2p_fetch_election.go`, `p2p_libp2p_fetch.go`, `p2p_reseed.go` | `go test -race ./app/p2p/` |
| **A5** — conflito de identidade libp2p bloqueia peer reiniciado | (1) **Identidade persistente**: chave Ed25519 carregada/criada em `<DataDir>/p2p/libp2p-identity.key` e passada via `libp2p.Identity` — peer.ID estável entre restarts; (2) **blocklist com TTL** (15 min) + `UnblockPeer` no gater (era append-only permanente); (3) **registry com lastSeen**: conflito só é sinalizado para registros recentes (janela 10 min); registro stale é substituído sem conflito; prune de entradas >24h | `src/app/p2p/p2p_libp2p_identity.go` (novo), `p2p_libp2p.go`, `p2p_libp2p_peer_registry.go` | `go test -race ./app/p2p/` |
| **A3** — tokens em texto plano em `%ProgramData%\Discovery` | **Correção (fase 1):** `platform.HardenSecretFileACL` aplica DACL explícita nos arquivos de segredo (SYSTEM/Admins full, Everyone RX, `/inheritance:r`) após escrita em `installer.Persist`, `debug.PersistConfig` e `chat.persistConfig` — somente quando `platform.IsElevated()` (não elevado = standalone: não aplica, preserva escrita da própria UI). **Fase 2 (DPAPI):** ver §5 | `core/platform/perms_windows.go`, `perms_other.go`, `installer/service.go`, `debug/service.go`, `services/chat/service.go` | vet + testes dos pacotes |
| **A12** — bootstrap NSIS com integridade fraca | (1) `.onInit`: override `/PU=`/`/PAYLOAD_URL=` só aceita `https://` (MessageBox + `Abort`); (2) fallback "Prosseguindo..." (endpoint dinâmico falho + hash estático ausente) virou **Abort** — stage2 nunca roda sem hash validado. Authenticode (WinVerifyTrust) fica como melhoria futura (§6) | `src/build/windows/installer/project.nsi` | revisão (NSIS não compilado nesta sessão — validar no CI) |
| **A13** — `/KEY=` em claro no `installer.log` | Linha `CommandLine: $CMDLINE` substituída por log estruturado sem segredos (`URL/AP/MINIMAL/UPDATE/GENERIC/KEY=<oculta>`); `ExecToLog` do stage2 não ecoa a linha de comando — o leak restante era a linha do header | `project.nsi` | revisão |

### Notas da correção C7 (análise solicitada)

- **Modelo declarado** (comentários originais em `touchP2PTempDir`/`p2pTempDir`): *serviço (SYSTEM) escreve, usuário comum lê/lança*. `Everyone:(F)` contrariava o próprio modelo.
- **Threat:** agente elevado executa instaladores de `%WINDIR%\Temp\Discovery\P2P_Temp`; com ACL `Everyone:F`, usuário local substituía o binário entre o checksum e o `CreateProcess` (TOCTOU → LPE para SYSTEM).
- **Fix:** `Everyone:(OI)(CI)RX` + Full para SYSTEM/Admins, SIDs por número (`*S-1-1-0`, `*S-1-5-18`, `*S-1-5-32-544`) — icacls com nome (`Everyone`) falha em Windows pt-BR (bug latente corrigido de quebra).
- **Compatibilidade:** modo standalone não elevado já não funcionava nesse diretório (não consegue criar/grantar sob `%WINDIR%\Temp`) — nenhuma regressão.
- **Restado (P2):** re-verificação de hash imediatamente antes do `CreateProcess` (abrir com share exclusivo e executar pelo handle) como defesa em profundidade para cenário admin hostil.

---

## 2. Avaliação C6 — arquitetura em dois apps (serviço + UI)

**Modelo atual (Ponto de partida — decisão D1 do PLANO_SEPARACAO_SERVICO_UI):**

| Binário | Papel | Identidade |
|---|---|---|
| `discovery-service.exe` (`cmd/discovery-service`) | Core completo: agentConn/NATS, inventário, sync, automação, P2P, self-update, outboxes. Sem UI (sem Wails/WebView2/tray) | Serviço Windows `DiscoveryAgent` (LocalSystem, auto) |
| `discovery-agent.exe` (UI Wails) | Interface do usuário; conecta ao serviço via IPC named pipe (modo **companion**); fallback **standalone** roda o core na própria UI se o serviço sumir por 5 min (`CompanionController`) | Sessão interativa do usuário |

**O que já está bom (manter):**
1. Separação sessão 0 / sessão interativa resolve os bloqueios de desktop (captura, SendInput, UIPI) — arquitetura correta para RMM.
2. IPC com request/response RPC (Fase C) e snapshot de status direto na conn de origem (revisão 2026-09-05).
3. Controller de fallback testável (`CompanionController` com `CompanionTuner` fake) — padrão a manter.
4. Binário de UI se auto-protege: se lançado como serviço SCM, registra erro e sai (nunca core+UI na sessão 0).

**Problemas e oportunidades identificados (priorizados):**

| # | Item | Risco/Hoje | Recomendação |
|---|---|---|---|
| 1 | **M5 — dois cores simultâneos**: o fallback standalone (`OnFallbackStandalone` → `runStagedStartup`) encerra o polling e **não há re-adoção** quando o serviço volta; pior: se o serviço volta durante o startup da UI, o IPC client vivo + core local iniciam juntos | Duplicação de agentConn/P2P/outbox no mesmo host (dois heartbeats, downloads duplicados, conflito de SQLite) | (a) No modo standalone, manter um **detector leve do serviço** (poll cada 60s com `IsServicePresent`); (b) ao detectar retorno: encerrar o core local de forma limpa e re-entrar em companion; (c) `runStagedStartup` deve recusar iniciar se serviço presente (corrida de fallback) |
| 2 | **Escritas de config pela UI em modo companion** | A UI (usuário) escreve config.json/debug/chat direto no disco em standalone; em companion deveria delegar ao serviço | Expandir o RPC IPC (Fase C) para cobrir **todas** as escritas (`SetChatConfig`, `SetDebugConfig`, `Persist`) — a UI nunca toca os arquivos em modo companion (alinha com A3: arquivo só é escrito por processo elevado → hardening de ACL sempre aplicável) |
| 3 | **Multiusuário/multi-UI** | Vários usuários logados → várias UIs conectam ao mesmo pipe; eventos/notificações vão para todas (por design A2 — UI para todos) | Definir contrato: notificações interativas (require_confirmation) → **sessão do console prioritária** (ou modal em todas com dedup por notificationID — já existe no svc de notificações); RDP desconectado → headless toast (já implementado via `NativeFallback`) |
| 4 | **Watchdog/recuperação do serviço** | Serviço travado = 5 min de janela morta na UI | NSIS: configurar recovery do SCM (restart on failure) + healthcheck no pipe (RPC `ping`) a cada tick do CompanionController |
| 5 | **Duplicação de lógica de startup** | `runStagedStartup` (UI standalone) vs `RunServiceMode` (serviço) divergem com o tempo | Extrair um `coreBootstrap` compartilhado (mesmos passos, flags explícitas por modo) e testes de contrato IPC já existentes (`ipc_contract_test.go`, `ipc_protocol_test.go`) como gate de CI |
| 6 | **Observabilidade do modo** | Dashboard não sabe se o agent está em companion/standalone | Expor `mode` no status (companion/standalone/service) e propagar ao servidor |

**Plano sugerido de execução (2 sprints):**
- **S1:** itens 1 e 4 (re-adoção + recovery/healthcheck) — fecham o M5 e a janela morta.
- **S2:** itens 2 e 5 (RPC completo de config + bootstrap compartilhado) — fecham a superfície que a fase 2 do A3 precisa (UI sem escrita direta) e endurecem o contrato.

---

## 3. Plano A2 — IPC multiusuário (decisão: UI é para todos os usuários)

**Restrição do produto (decisão do dono):** a interface do agent deve funcionar para **todos os usuários** da máquina — o SDDL `D:(A;;GA;;;SY)(A;;GA;;;BU)` permanece. Melhorias propostas (sem quebrar o requisito):

| # | Melhoria | Efeito | Esforço |
|---|---|---|---|
| 1 | **Handshake autenticado por token de sessão**: serviço gera token efêmero por sessão (file com ACL SYSTEM F / Everyone RX em `%ProgramData%Discoverysession-<id>.token`); UI lê e envia no `hello`; mensagens **mutantes** (notification:respond, remote session, command_result) só são aceitas com token válido. RPCs de leitura continuam abertos (não quebra o requisito multiusuário) | Fecha a injeção de confirmações/remote-session por processo arbitrário sem restringir acesso | Médio |
| 2 | **Auditoria do chamador**: `GetNamedPipeClientProcessId` + `OpenProcess(PROCESS_QUERY_LIMITED)` no handshake para logar PID/user de cada UI (auditoria de quem respondeu o quê) | Rastreabilidade e detecção de abuso | Baixo |
| 3 | **Rate limit por conn** nas mensagens mutantes (ex.: máx 10/s) e dedup de correlationID | Blinda contra flood | Baixo |
| 4 | **SDDL por canal** (fase 2): dois pipes — leitura (BU) e mutação (SDDL restrito à sessão do console + SYSTEM) | Reduz ainda mais a superfície sem retirar o requisito | Médio |

**Nota:** com o item 1, o achado A2 fica efetivamente mitigado mantendo o comportamento multiusuário.

---

## 4. A3 — Fase 2: DPAPI (design a definir em conjunto)

- **Objetivo:** tirar os tokens de texto plano (config.json, debug_config.json, chat_config.json) mesmo da leitura por usuários locais.
- **Desafio de escopo:** DPAPI *user-scope* não cruza identidades (instalador admin ↔ serviço SYSTEM ↔ UI usuário); *machine scope* não impede leitura por usuários locais. Proposta: criptografar com **machine scope + DACL de leitura restrita** combinada ao hardening da fase 1, OU migrar a leitura para IPC (o serviço guarda e distribui tokens sob demanda — C6/S2).
- **Migração:** leitura legado (plaintext) continua válida; na primeira escrita o arquivo migra para o formato criptografado (`{"__dpapi": true, "data": "<b64>"}`).
- **Dependência:** recomenda-se resolver o item 2 do C6 (UI sem escrita direta em modo companion) antes.

---

## 5. Itens postergados (decisão do dono)

| Item | Motivo |
|---|---|
| **C3** (auth no transporte libp2p) | Autenticação será implementada em momento posterior |
| **C4** (HMAC do onboarding com deployKey) | **Intencional:** parque grande + formatação de máquinas exige provisioning por qualquer agent já configurado (zero-touch). Restrição/segurança virão em ajuste futuro próximo |
| **C5** (bridge HTTP de debug expõe tudo) | **Intencional:** expor tudo para debug/testar os recursos da interface pelo navegador quando necessário |
| **A4** (`/p2p/config/onboard` sem auth) | Par do C4/C3 — tratado junto com a autenticação P2P |
| **A8** (Authenticode morto + MotW) | Requer elaboração maior (assinatura real dos artefatos no CI antes de ligar a validação) — fase futura; ligar à assinatura de código (M24) |
| **A10** (token em `nats://` claro) / **A11** (TLS pinning não aplicado) | Requerem decisão de topologia/transporte (TLS do NATS, WSS preferencial) — fase de hardening de transporte |
| **A12** (Authenticode do stage2) | Melhoria futura junto com M24 (assinatura de código) |
| Demais M/B | Tratados em outro momento (conforme conversa) |

---

## 6. Verificação executada (2026-09-10)

- ✅ `go vet ./...` — **limpo, exit 0** (módulo inteiro).
- ✅ `go test ./app/... -count=1` — **100% OK, exit 0** (todos os pacotes com testes passaram, incl. `app`, `selfupdate`, `notifications`, `automation`, `mcp`, `terminal`, `p2p`).
- ✅ `go test -race` nos pacotes `p2p` e `mcp` (achados A7/A16) — OK.
- Testes novos: `selfupdate/updater_network_test.go`, `services/notifications/service_test.go`, `core/terminal/shell_validate_test.go`.
- **NSIS:** correções de A12/A13 são editadas mas **precisam passar pelo build do instalador no CI** (NSIS não disponível nesta sessão).
- **Build do agente:** lembrar que build de produção é sempre via Wails (`wails build`), não `go build`.

---

---

## 7. Achados Baixos (B1–B23) — avaliação e correções (2026-09-10)

**Corrigidos (19):** B1, B2, B3, B4, B5(parcial), B6, B7, B8, B9, B10, B11, B12, B13, B14, B15, B16, B17, B18, B19, B23.

| Item | Correção | Arquivos |
|---|---|---|
| B1 | Token ≤8 chars ia inteiro para o log persistido → agora mascarado por completo (`***`) | `inventory/sync.go` |
| B2 | Get devolvia a API key integral (comentário dizia "masked") → sentinel `********`; **round-trip corrigido**: Set com chave vazia/mascarada preserva a armazenada (antes, salvar outros campos apagava a chave); Test com sentinel resolve a real | `services/chat/service.go` |
| B3 | `Broadcast` segurava `s.mu` durante escritas de rede (3s/conn) → snapshot sob lock + escrita fora + mutex de escrita **por conn** (`ipcClientConn.writeTo`), aplicado a Broadcast, RespondTo e hello_ack — fim da serialização por zumbi e da corrida Broadcast×RespondTo no framing | `ipc_windows.go` |
| B4 | Goroutines cruas sem recovery → `a.safeGo` (post-bootstrap + hideWindowOnStartup) | `app.go` |
| B5 | `pollDropped` nunca incrementado → incrementa no drop de inscrito SSE lento. `BuildArtifactAccess` reavaliado: **tem 2 callers** (libp2p transport + GetArtifactAccess) — não é código morto; a URL `/p2p/artifact` sem rota HTTP fica pendente no escopo do C3 (infra de token) | `debughttp/broker.go`, `p2p_http.go` (sem mudança) |
| B6 | Mensagem do usuário ficava no histórico em falha do request sync (reenvio duplicava) → pop em erro | `core/ai/chat.go` |
| B7 | Erro de `SetAgentAuthHeadersWithAgentID` engolido → falha cedo com log no `round_http_error` | `core/ai/chat_multi_round.go` |
| B8 | `truncateToolResult` fechava brackets antes da aspa → JSON inválido → agora fecha LIFO correto (aspa primeiro) | `core/ai/chat_multi_round.go` |
| B9 | `cachedAt` nunca lido (TTL inexistente) + sem single-flight → janela `minRevalidateInterval=5min` + dedup com `inflightMu.TryLock()`; testes 304 ajustados + teste single-flight novo (`-race` OK) | `core/data/http_client.go` + testes |
| B10 | Retry em todo erro re-aplicava POST/PUT (dupla submissão) → só métodos idempotentes (GET/HEAD/OPTIONS), inclusive para 5xx/429 | `core/tlsutil/retry.go` |
| B11 | `started`/`ctx`/`cancel` acessados sem lock → mutações/leituras sob `c.mu` em Startup/Shutdown/Ctx | `p2p/p2p.go` |
| B12 | GET do transporte libp2p servia `.partial` e sidecars → rejeita `.partial`/`.meta`/`.meta.json` | `p2p_libp2p_transport.go` |
| B13 | `@import` de Google Fonts + preconnects em cada load → removidos (agente enterprise offline; fallbacks de sistema já nos stacks) | `styles/base.css`, `index.html` |
| B14 | `status.color` do servidor ia para `style` inline sem neutralizar `;`/`:` → `safeCssColor` (allowlist hex/rgb) + `safeStatusBadgeStyle`; aplicado no badge do card e no detalhe | `app-utils.js`, `app-support.js` |
| B15 | try/catch síncrono em promise não aguardada → `.catch` (`SetExportRedaction`, `AnswerChatQuestion`); `app-p2p.js` já estava async/await | `js/app-init.js`, `js/app-chat.js` |
| B16 | `lines.join("\n")` juntava com literal → newline real | `js/psadt-debug.js` |
| B17 | `setInterval` sem handle acumulava a cada re-mount → handle + clear dup | `js/p2p-debug.js` |
| B18 | `startThinkingStatusUpdates` era dead code (sem callers; se ativa, GetLogs a cada 900ms) → removida; `stopThinkingStatusUpdates` mantida (ui:suspend) | `js/app-chat.js` |
| B19 | `escapeHtmlAttr` apagava backticks (corrompia valores) → escapa `&#96;` | `js/app-utils.js` |
| B23 | Dispatcher podia ficar órfão em `Accept()` eterno após crash do agente (zumbi segurando named pipes) → monitor de morte do pai: `parentPID()` via Toolhelp32 + `OpenProcess(SYNCHRONIZE)` + `WaitForSingleObject` → `os.Exit(0)` libera pipes/ConPTY | `core/terminal/dispatcher_windows.go` |

**Incidente durante a execução (registrado p/ transparência):** ao anexar os helpers do B14, o `app-utils.js` foi regravado a partir de uma leitura limitada e ficou truncado; recuperado de `HEAD` via `git checkout` e as edições reaplicadas com append pontual (`Add-Content`) — sintaxe validada com `node --check` em todos os JS editados.

**Postergados com justificativa:**
- **B20** (pin de choco/MinGW nos workflows): mudança de CI não é validável nesta sessão; parear com M22 (pin de actions por SHA) e M24 (assinatura) numa rodada de CI dedicada.
- **B21** (config.json montado por concatenação no NSIS): escaping de `"`/`\` em NSIS requer plugin de string (StrFunc) e build do instalador para validar — incluído na próxima rodada de instalador (junto com A12/A13/M24).
- **B22** (`installer.json` com `${API_KEY}` em heredoc): script de build do **servidor** (repo da API/portal); consertar exige decidir ferramenta de escape (jq) e testar o build Linux — e a questão do secret em artefato plano merece decisão de pipeline (não é só escaping).

### Verificação da rodada B (2026-09-10)
- ✅ `go vet ./...` exit 0 · ✅ `go test ./app/... -count=1` 34 pacotes OK, 0 FAIL · ✅ `-race` em data/p2p/mcp nas rodadas anteriores.
- ✅ `node --check` nos 7 arquivos JS de frontend editados.

---

## 8. Otimizações de performance (2026-09-10)

### M19 — `a2ui/node_modules` fora do embed (exe ~78 MB → ~47 MB)

**Contexto do produto (intenção preservada):** o A2UI existe para o LLM do chat de
suporte montar interfaces dinâmicas (surfaces com botões, forms, tabs, cards…) e
receber as ações do usuário — **nada da funcionalidade foi removido**. O runtime
continua carregando `frontend/a2ui-bundle.js` (455 KB, IIFE autocontido com o
renderer @a2ui/lit + @a2ui/web_core) via `<script>` — exatamente como antes.

**Causa raiz:** `frontend/a2ui/` é a *toolchain de build* do bundle (package.json,
entry.js, build.mjs + node_modules com 31,3 MB / 7.795 arquivos). O `node_modules`
nem é versionado (está no `.gitignore` do a2ui) — ele existia no disco por
`npm install`. Como `main.go` faz `//go:embed all:frontend`, qualquer coisa nesse
caminho vai para dentro do executável: **96% do frontend embedado era toolchain**
(32,7 MB de 34,2 MB), inflando o exe de ~47 MB para ~78 MB.

**Correção:** a toolchain foi movida para `src/tools/a2ui/` (fora do caminho do
embed):
- `git mv frontend/a2ui tools/a2ui` (package.json, entry.js, build.mjs, .gitignore)
- `build.mjs` agora aponta o `outfile` para `../../frontend/a2ui-bundle.js`
- comentários/.gitignore atualizados com o novo fluxo (`cd src/tools/a2ui && npm install && npm run build`)
- **Prova:** `node build.mjs` regenerou o bundle (454,7 kb, sintaxe OK,
  `window.A2uiChat` presente — footer do esbuild mudou de posição, sem efeito)
  e o bundle commitado foi restaurado para o commit ficar limpo (apenas o move)

**Resultado medido:** frontend embedado **32,7 MB → 1,41 MB** (94 arquivos) —
exe cai ~31 MB. O `wails build` de produção deve confirmar os ~47 MB finais.

### Melhoria 16 — `sync.Pool` para buffers de frame GDI/DXGI

**O problema (medido na leitura do código):** cada frame de tela é um buffer
BGRA de `width*height*4` — ~8,3 MB @1080p. Antes da otimização havia **2-3
alocações dessas POR FRAME**: (1) o capturador (GDI, god3d E DXGI manual faziam
`make([]byte, w*h*4)` a cada `AcquireNextFrame` — o comentário do
session_screen sobre `c.img.Pix` reutilizado estava desatualizado); (2) o copy
para o encode worker em `session_screen.go` (outro `make` por frame). A 30 fps:
**~250-500 MB/s de churn de GC** — pressão de GC constante, spikes de CPU do
coletor durante sessão remota ao vivo.

**Correção em 3 camadas:**

| Camada | Antes | Depois |
|---|---|---|
| Capturadores (GDI/god3d/DXGI manual) | `make([]byte, w*h*bpp)` por frame | Buffer `frameBuf` reutilizável por capturer (`cap` cresce se resolução subir) |
| Copy → encode worker (`session_screen.go`) | `make([]byte, len)` por frame | `screen.GetPooledFrame()` do novo `screen/frame_pool.go` |
| Encode worker | frame morria (GC) | `screen.PutPooledFrame(job.frame)` recicla para o próximo copy |

**Garantias de segurança do pool (`frame_pool.go`):**
- Contrato do Frame inalterado: Data válido até o próximo Acquire — o consumidor
  já copiava antes do próximo acquire (mesmo padrão do go-d3d legado).
- Dirty detector é seguro: mantém **cópia própria** (`d.lastFrame`) do frame
  anterior, nunca referência ao buffer do frame (verificado em `dirty_rects.go`).
- `Get` descarta buffers com cap incompatível (menor que o necessário ou >2×,
  para não reter memória de resoluções antigas — 4K→1080p não segura 33 MB).
- `Put` com guardas: nil/Data vazio/limite de 64 MB (buffer HDR scRGB 4K ~66 MB
  não fica retido).
- `sync.Pool` esvazia em GC: sem retenção permanente; frames em voo no shutdown
  sem devolução são recolhidos pelo GC normalmente.

**Testes:** `frame_pool_test.go` — dimensões, reciclagem (mesmo endereço no
ciclo Get→Put→Get com drenagem prévia), descarte em mudança de resolução
(4K→480p), guards do Put. `-race` OK; suíte completa 34 pacotes OK / 0 FAIL.

**Ganho esperado:** eliminação de ~16 MB/frame de alocações no caminho GDI e
~8 MB/frame no caminho DXGI/god3d → churn de GC da sessão remota cai de
~250-500 MB/s para **próximo de zero em regime permanente** (buffers circulam
no pool).

---

## 9. Achados Médios (M1–M34) — avaliação e correções (2026-09-10)

**Corrigidos: 31** (M1-M3, M6-M14, M16-M18, M20, M23-M28, M30-M34 + M19 anterior). **Postergados: 2** com justificativa (M22, M24). M4/M5/M8/M15/M29 implementados nas rodadas de decisão do dono (tabelas abaixo).

| Item (adicional) | Correção | Arquivos |
|---|---|---|
| M20 | Inputs de workflow_dispatch interpolados direto no `run` (command injection pelo formulário) → mapeados para `env:` e lidos via `$env:WF_*` nos 2 workflows de build | `.github/workflows/build-agent-bootstrap.yml`, `build-agent-installer.yml` |
| M21 | Chave do servidor como input plaintext (visível no histórico da run) → `secrets.AGENT_DEFAULT_KEY` com input como fallback vazio e input marcado DEPRECIADO | `.github/workflows/build-agent-installer.yml` |
| M23 | `permissions: contents: write` no nível do workflow → `permissions: {}` no raiz e `contents: write` apenas no job `release` | `.github/workflows/release-agent-on-tag.yml` |

| Item | Correção | Arquivos |
|---|---|---|
| M1 | Diff de inventário sempre detectava mudança (timestamps voláteis no payload byte-a-byte) → `stripVolatileInventoryJSON` remove `updatedAt`/`collectedAt`/`inventoryCollectedAt` dos dois lados antes de comparar (fingerprint estável) | `core/database/sqlite_cache_inventory.go` |
| M2 | Toast headless: `Metadata["actions"]` carregava `[]AgentNotificationAction` concreto que nunca casava com `.([]any)` → normalizado na fonte (`service.go` monta `[]any` de maps com id/label/value) e o toast aceita ambos os tipos. Botões do toast no modo serviço voltam a funcionar | `services/notifications/service.go`, `native_toast_windows.go` |
| M3 | Callback do `time.AfterFunc` lia `ds.deferCount/ds.maxDefers` fora do lock → snapshot capturado sob `ds.mu` e usado no callback | `app.go` |
| M6 | `--agent-delete-cleanup` (DELETE remoto do agente) invocável por qualquer usuário local → exige processo elevado (`platform.IsElevated`); exit 1 com log caso contrário | `main.go` |
| M7 | Redirect podia apontar para qualquer host (SSRF pivot) + DNS TOCTOU → `CheckRedirect` revalida a allowlist em cada salto; `DialContext` próprio resolve o host e diala SOMENTE IP validado na allowlist (pinça o IP efetivo; hostname preservado para SNI). Novo `Allowlist.ContainsIP` | `core/netproxy/proxy.go`, `allowlist.go` |
| M9 | Chat logger acumulava interações (user/assistant/tool args) em texto plano sem limite → rotação por tamanho (10 MB), retenção de 7 dias e teto de 7 backups | `core/ai/chat_logger.go` |
| M10 | Fallback NATS: uma goroutine por comando (execução até 2min) sem limite → semáforo de 8 execuções concorrentes com backpressure natural no handler | `core/agentconn/runtime.go`, `runtime_nats.go` |
| M11 | Stream de replicação sem deadline (worker travava para sempre) e `resp.Gone` tratado como sucesso → `SetDeadline(10s)` + Gone é falha com log | `p2p/p2p_replication.go` |
| M12 | Artifact "failed" re-elegia e baixava a cada 60s indefinidamente → backoff exponencial por FailCount (1m→30m, teto), `NextAttemptUTC` respeitado no `runPendingElections`, reset no sucesso | `p2p/p2p_fetch.go`, `p2p_fetch_election.go` |
| M13 | `icacls.exe` spawnado em TODA chamada de `p2pTempDir()`/`touchP2PTempDir()` (gossip 45s, GetStatus sob RLock) → `sync.Once` por processo nos 2 sites | `app/p2p_cleanup.go`, `app/p2p/p2p.go` |
| M14 | Hash do arquivo rodava DENTRO do `sha256CacheMu` (serializava ListArtifacts) → compute fora do lock com double-check; `manifestMatchesFile` re-hash por chamada → cache do health-check por (path,mtime,size) invalidado no Delete | `p2p/p2p.go`, `p2p_publish.go`, `p2p_cache.go` |
| M16 | `/p2p/health` público expunha hostname do Windows (recon) → removido (peer usa o IP alvo); LAN probe /24 a cada 2min → 5min | `p2p/p2p_lan_probe.go`, `p2p/p2p.go` |
| M17 | Card de ticket com JSON não-parseável: catch vazio, zero feedback → log de erro + feedback visual ao usuário (chave nova `support.ticketLoadError` PT/EN) | `js/app-support.js`, `js/app-utils.js` |
| M18 | Bridge HTTP de debug descartava argumentos além do primeiro E o servidor rejeitava multi-param → servidor aceita ARRAY JSON com bind posicional; client manda objeto (1 arg) ou array (N args) | `debug_http.go`, `js/debug-http-bridge.js` |
| M25 | Taskfile buildava sem `-X buildinfo.Version` → "0.0.0" (risco de loop de self-update) → ldflags injetam Version (default `0.0.0-dev`, override `-a APP_VERSION=`) e Commit (git rev-parse) no UI e no serviço | `build/windows/Taskfile.yml` |
| M26 | Panic em job do cron derrubava o processo (sem Recover) + guards de nil inconsistentes → `cron.WithChain(cron.Recover(...))` + guards `db != nil` nos 3 blocos do callback | `core/automation/service.go` |
| M27 | `syscall.NewCallback` em `GetMonitors` a cada 5s (callback permanente, ~2000 limite) → crash em 2-3h de sessão → callback ÚNICO por processo + acumulador global sincronizado | `core/screen/monitor.go` |
| M28 | `zipDirProgress`/`copyDir` seguiam junctions → recursão infinita/cópia fora do sandbox → `isReparsePoint` aplicado nos loops (pula links) | `core/fileserver/server.go` |
| M30 | RunScript executava sem verificar `ContentHashSHA256` (payload corrompido/adulterado executava) → verifica SHA256 do content cru (algoritmo espelhado do servidor: `SHA256(UTF8(content))` hex lower, confirmado em `AutomationScriptService.cs:368`) | `core/automation/executor.go` |
| M31 | Tools destrutivas do MCP sem gate (`uninstall_package`, `upgrade_all_packages`, `restart_spooler`, `clear_queue`) → parâmetro `confirm=true` obrigatório + descrição marcada DESTRUTIVA (o LLM precisa da aprovação real do usuário) | `core/mcp/register.go` |
| M32 | cmdType desconhecido executava via `cmd /C <command>` genérico → rejeitado com erro claro | `core/agentconn/runtime_protocol.go` |
| M33 | Gravação de tela nunca iniciava: `handleRecordingStart` só setava flag + evento enganoso → chama `RecordingSource.Start()/Stop()` reais na sessão de tela (frames passam a fluir no subject `.recording`) | `core/remotesession/manager.go` |
| M34 | Cron de automação na TZ local sem contrato → `cron.WithLocation(UTC)` (contrato do servidor: datas ISO-8601 UTC) + override `DISCOVERY_AUTOMATION_TZ` | `core/automation/service.go` |

**Postergados com justificativa:**

- **M22** (pin de SHA das actions): precisa dos SHAs reais e validação de CI — parear com M24/B20.
- **M24** (assinatura de código): requer certificado de codesigning — decisão comercial/compliance.

### M4/M5/M15/M29 — implementados após decisão do dono (2026-09-10)

| Item | Decisão do dono | Implementação |
|---|---|---|
| **M15** | Opção A (bucket global bidirecional); configurável via servidor, de 10 em 10 MB/s limitado a 100 MB/s; default sem limite | `p2p/bandwidth.go` (novo): token bucket global com burst 1s (`ConfigureP2PBandwidth`/`p2pBandwidthWait`) + `bandwidthThrottleReader`. Clamp em `NormalizeConfig` (0=sem limite; >0 floor para múltiplo de 10 MB, [10,100] MB/s). Aplicado no **sender** (`handleStreamArtifactGet`, bytes servidos) e no **receiver** (`libp2pDownloadChunk`, bytes baixados) |
| **M5** | UI somente interface — **sem fallback standalone** | `app.go`: startup da UI nunca abre DB nem roda core (`runStagedStartup` ficou exclusivo do serviço via `runCoreStartup`); `startIPCClient` sempre roda e aguarda o serviço (RunConnectLoop com backoff). `companion_controller.go` reescrito: MaxFailures → `OnServiceLost` 1x e **continua polling**; novo `OnServiceRecovered` quando o serviço volta (testes atualizados + novo teste de recovered). `decideCompanionMode` removida (sem uso) |
| **M4** | Opção B com 5s | `app.go` shutdown: timeout 3s→**5s** + contador `startupPhasePending` (atomic) nas 4 fases — o log informa QUANTAS fases ficaram pendentes ao fechar o SQLite; queries em andamento falham graciosamente (WAL preserva integridade) |
| **M29** | Disco inteiro mantido (feature) + **auditoria** local e no servidor quando habilitado | `fileserver/server.go`: `SetAuditHook` + `auditOperation` nas ações mutantes (put/delete/rename/mkdir/move/copy/unzip) com gate `DISCOVERY_FILES_AUDIT` (default on); `session_files.go` injeta hook que faz `log.Printf` local + evento `files_audit` ao servidor via NATS (action/path/newPath/ok/error/timestamp) |
| **Loop self-update** (análise de logs de homologação 2026-09-13 — não era um achado do relatório; binário de produção compilado sem `-X buildinfo`) | Circuit breaker persistido no self-update: após **3 instalações da MESMA versão** sem o buildinfo mudar, checks pausam com log CRÍTICO; guard limpa-se quando a versão do servidor muda ou o buildinfo passa a reportar corretamente | `core/selfupdate/updater_guard.go` (novo), `updater.go`, `updater_guard_test.go` |
| **Contrato de update** (decisão do dono: version+commit da API) | O check agora compara a versão/commit **EFETIVOS**: buildinfo confiável tem precedência; se o binário reporta `0.0.0`/`unknown` (build sem `-X buildinfo`), usa o **marker do último install confirmado** (`selfupdate-installed.json`) — gravado quando o installer.log confirma sucesso / commit muda / versão resolve. Pending state agora grava `InstalledCommit = serverCommit` (o commit instalado, não o do processo antigo). Fallback (sem /version) usa a versão efetiva. Contrato: versão dif → update; versão igual + commit dif → update (rebuild); ambos iguais → skip; commits incomparáveis + versão igual → skip (anti-loop). Testes de contrato novos | `core/selfupdate/updater_marker.go` (novo), `updater.go`, `updater_network.go`, `updater_marker_test.go` |
| **CAUSA RAIZ no servidor**: `AgentPackageService.cs` compilava o agente com `-X discovery/internal/buildinfo.Version` — caminho NÃO existe (correto: `discovery/app/core/buildinfo`) → linker ignora silenciosamente → binário sempre 0.0.0/unknown. Confirmado via SSH no servidor: `grep a6ddadd4` = 0 no instalador 1.2.1; commit do DB (`76d554c9`) ≠ binário. **Fix:** caminho corrigido + Commit agora injetado no discovery-service.exe também (era só Version) | `DiscoveryRMM_API/.../AgentPackageService.cs` |
| **Remote session: input em janelas elevadas + tela de logon** (homologação 13/09) | Sessão remota rodava NO PROCESSO DA UI (companion, Medium integrity) → UIPI bloqueava SendInput em janelas elevadas (Gerenciador de Tarefas) e a UI não acessa o desktop de logon. **Fix (decisão do dono — M5 "interface somente interface"): serviço SEMPRE spawn o worker (SYSTEM na sessão interativa) para remote session** — nunca broadcast para a UI; `acquireInteractiveSessionToken` agora prefere o token SYSTEM com sessão ajustada (worker SYSTEM em winsta0\default ou winsta0\winlogon) | `remote_debug_commands.go`, `ipc_app_integration.go`, `remote_session_worker_spawn.go` |
| **Chave identidade P2P** (consequência da correção A5) | `libp2p-identity.key` é chave PRIVADA binária (68 bytes protobuf Ed25519) em `%ProgramData%\Discovery\p2p\` — o conteúdo é binário por design (editores exibem "caracteres orientais" aleatórios; NÃO é texto/corrupção de encoding). ACL endurecida via `HardenSecretFileACL` quando elevado (mesmo padrão A3) | `p2p/p2p_libp2p_identity.go` |
| **M8** | Dados do `discovery.db` são **descartáveis** (cache) — migração = drop-and-recreate | `core/database/sqlite.go`: versionamento com `PRAGMA user_version` (`currentSchemaVersion = 2`); ao abrir, se a versão diverge da atual, o schema antigo é **descartado** (DROP TABLEs do sqlite_master) e recriado do zero — o agente reprocessa/repopia. Mudanças puramente aditivas (CREATE IF NOT EXISTS) preservam dados sem incrementar versão. Testes: migração de banco legado v1 + preservação em versão atual (`sqlite_migrate_test.go`) |

### Verificação da rodada M (2026-09-10)
- ✅ `go vet ./...` exit 0 · ✅ `go test ./app/... -count=1 -p 1` 34 pacotes OK / 0 FAIL (flaky pré-existentes confirmados: 2 falhas sob execução paralela pesada passam isoladas e sequencial).
- ✅ `node --check` nos JS editados (app-support, debug-http-bridge, app-utils).

### Histórico
| Data | Nota |
|---|---|
| 2026-09-10 | Fase 1 das correções aprovadas + avaliações C6/A2 |
| 2026-09-10 | Rodada B: 19/23 achados Baixos corrigidos; B20-B22 postergados com justificativa |
| 2026-09-10 | Otimizações: M19 (a2ui fora do embed) + sync.Pool de frames (GDI/DXGI/session_screen) |
| 2026-09-10 | Rodada M: 26 achados Médios corrigidos (incl. CI M20/M21/M23); 7 postergados com justificativa |
| 2026-09-10 | Decisões do dono implementadas: M15 (bucket global 10-100 MB/s via servidor), M5 (UI sem fallback), M4 (5s + contador), M29 (auditoria local+servidor) |
| 2026-09-10 | M8: versionamento de schema SQLite (PRAGMA user_version) com drop-and-recreate — dados de cache descartáveis (decisão do dono) |
| 2026-09-13 | **Análise de logs de homologação**: loop de self-update confirmado — agente em produção reporta `buildinfo.Version=0.0.0`/`commit=unknown` (binário compilado sem `-X buildinfo` no servidor); servidor oferece 1.2.1 → instala com sucesso → reinicia → binário reinstalado continua 0.0.0 → check reinstala (5 launches em ~1min). **Circuit breaker implementado** no self-update (guard persistido em TempDir: após 3 instalações da MESMA versão sem o buildinfo mudar, checks pausam com log crítico até o build ser corrigido) + teste unitário. **Ação operacional:** recompilar o binário do servidor com `--version` (build-agent-server-linux.sh injeta corretamente quando informado) e reinstalar |
| 2026-09-13 | **Contrato de update implementado** (decisão do dono): comparação version+commit da API com versão/commit EFETIVOS (marker do último install confirmado quando o buildinfo não é confiável); pending grava o commit instalado; skip apenas quando version+commit coincidem. Testes de contrato |
| 2026-09-13 | **Remote session: input em elevadas + tela de logon** — sessão remota movida da UI companion para o worker spawnado pelo serviço (SYSTEM na sessão interativa); token SYSTEM preferencial no spawn |
| 2026-09-13 | **Causa raiz do loop confirmada via SSH no servidor**: `AgentPackageService.cs` compilava com `-X discovery/internal/buildinfo` (caminho inexistente); binário 1.2.1 não tinha commit nem versão. Fix no caminho + Commit injetado no discovery-service |
