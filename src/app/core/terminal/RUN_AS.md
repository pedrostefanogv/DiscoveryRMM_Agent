# Run-As do Terminal Remoto (SYSTEM ↔ Usuário)

## Contrato

- `session.Meta["runAs"]` (payload da sessão `kind=terminal`): `"user"` (default) ou `"system"`. Valores inválidos → `user` com aviso no log.
- `term.ready["runAs"]` (aditivo): identidade **efetiva** — `"user"`, `"system"` ou `"inherit"` (fallback quando não há usuário logado).

## Comportamento

| Meta `runAs` | Usuário logado | Identidade do shell | Log |
|---|---|---|---|
| `user` | sim | `CreateProcessAsUserW` com token primário da sessão de console + `CreateEnvironmentBlock` | `[terminal] ConPTY shell spawnado como user <domain\\name>` |
| `user` | não | identidade herdada (SYSTEM no serviço) | aviso `runAs=user sem usuário logado` |
| `system`/herdado | — | herda o processo do agente | — |

- ConPTY: `CreateProcessAsUserW` com `STARTUPINFOEX` + atributo `PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE` + `STARTF_USESTDHANDLES` (como antes) + `lpEnvironment` do usuário.
- Legacy: `SysProcAttr.Token` (`CreateProcessAsUser` via `exec`).
- Dispatcher: flag `--terminal-runas=`; o dispatcher (filho, ainda SYSTEM) resolve o token localmente. Falha recorrente do `CreateProcessAsUserW` degrada para herdado nas tentativas seguintes (tryConPTY).
- **Job Object** `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` em ConPTY e legacy: filhos do shell morrem no `Close()` mesmo quando o Kill direto falha.
- Token: `WTSGetActiveConsoleSessionId → WTSQueryUserToken → DuplicateTokenEx(TokenPrimary)`, cache TTL 30s (dono do handle = cache; o shell só usa até o CreateProcess). Nenhum token vem do servidor — sem escalada de privilégio.

## Arquivos

- `core/terminal/runas_windows.go` / `runas_other.go` — tipos RunAs, token, env block
- `core/terminal/job_windows.go` — Job Object
- `core/terminal/pty_conpty_shell.go` — CreateProcessAsUserW + env + job
- `core/terminal/iserial_windows.go` — `NewShellInteractiveRunAs` (escada ConPTY→dispatcher→legacy)
- `core/terminal/pty_windows.go` — `NewShellWithToken` (legacy)
- `core/terminal/pty_windows_dispatcher_client.go` / `dispatcher_windows.go` — flag `--terminal-runas`
- `core/remotesession/session_terminal.go` / `manager.go` — Meta `runAs`, `StartRunAs`, `term.ready.runAs`

## Comportamento no remote-session-worker (não-SYSTEM)

O worker da sessão remota roda **como o próprio usuário** (spawnado via
CreateProcessAsUser na sessão interativa). Nesse contexto:

- `runAs=user` ≡ identidade herdada — o shell JÁ nasce como o usuário. O
  código detecta processo não-SYSTEM e resolve **sem** tentar
  `WTSQueryUserToken` (que sempre falharia sem SeTcbPrivilege e poluiria o
  log a cada retry).
- `runAs=system` no worker **não** eleva (não há SeTcbPrivilege) — também
  herda; o log registra a identidade real.

## Diagnóstico 2026-10-26 — terminal morto após deploy

Sintoma: toda sessão de terminal morria no startup (ConPTY "morreu
prematuramente" em 100% das tentativas, in-process e no dispatcher) → o
console fechava → o viewer recriava a sessão em loop (start/stop no console
do browser).

Causa raiz: **kill de injeção de DLL (0xC0000142) no boot dos filhos do
ConPTY** pelo AV/EDR da estação — reproduzido fora do agente com
`cmd/conptyprobe` (sobrevivência ~40% sob rajada; pior com spawns rápidos).
NÃO é causado pelo código run-as/Job Object (o Job Object, aliás, NUNCA
chegou a funcionar: cbSize 152 rejeitado pelo kernel com ERROR_BAD_LENGTH).

Correções aplicadas:
1. `runAs=user` no worker não-SYSTEM resolve sem tentar WTSQueryUserToken
   (fim do spam de log por retry).
2. Job Object com cbSize correto (144) — agora o KILL_ON_JOB_CLOSE funciona;
   opt-out de diagnóstico: `DISCOVERY_TERM_NO_JOB=1`.
3. Backoff progressivo nos retries (150ms → 1s) — evita a rajada de spawns
   que aciona o bloqueio comportamental do AV.
4. Exit code do filho logado na morte prematura (`exitCode=0x........`) —
   0xC0000142 = injeção de DLL; outros códigos apontam para outra causa.

Uso do probe: `cd src && go run ./cmd/conptyprobe <job|nojob>` — mede a
sobrevivência do ConPTY na máquina fora do agente.
