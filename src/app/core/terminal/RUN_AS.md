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
