# Autorização de acesso remoto (agente)

## Comportamento
- Somente `kind=screen|all`.
- `consent.mode=prompt`: prompt bloqueante com timeout; `timeoutAction`
  decide aprovar/negar quando ninguém responde.
- `notify`: banner/aviso sem bloquear.
- Sem usuário interativo: aplica `timeoutAction` imediatamente.
- Prompt primário: PSADT (`Show-ADTInstallationPrompt`, botões Permitir/Negar,
  `-NoExitOnTimeout`). Fallback: `MessageBoxTimeoutW` topmost. Se ambos falharem,
  **nega** (fail closed).
- Decisão enviada por HTTP com `SetAgentAuthHeadersWithAgentID`; falha de reporte
  impede o início da sessão.

## Banner
- Janela Win32 topmost no topo da tela, operador + tempo + Desconectar.
- `SetWindowDisplayAffinity(WDA_EXCLUDEFROMCAPTURE)`: NÃO aparece no viewer nem na
  gravação. Se a API falhar, o banner não é exibido (evita vazar no stream).
- Clique em Desconectar fecha a sessão local e reporta stop ao servidor.

## Arquivos
- `app/core/remotesession/consent.go`, `consent_gate.go`
- `app/remote_consent_prompt.go` (prompt + report)
- `app/core/screen/session_banner_windows.go`
- `app/remote_session_worker.go` (wiring)
