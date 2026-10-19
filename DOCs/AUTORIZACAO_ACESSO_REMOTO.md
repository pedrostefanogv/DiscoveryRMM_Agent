# Autorização de acesso remoto (agente)

## Comportamento
- Somente `kind=screen|all`.
- `consent.mode=prompt`: prompt bloqueante com timeout; `timeoutAction`
  decide aprovar/negar quando ninguém responde.
- `notify`: banner/aviso sem bloquear.
- Sem usuário interativo: aplica `timeoutAction` imediatamente.
- Prompt **principal (nativo, desenhado pelo agente)**: cartão Fluent com o tema do
  Windows do usuário, **contagem regressiva** `mm:ss` + barra de progresso e a
  **ação padrão explícita** (`Se você não responder, o acesso será NEGADO/PERMITIDO
  automaticamente`), rótulos Permitir (verde) e Negar, Enter confirma a ação padrão
  e Esc/X recusa. É o único backend que mostra o tempo restante E o resultado do
  timeout.
  > `Show-ADTInstallationPrompt` (PSADT) não desenha contador — só os diálogos de
  > Welcome/Restart têm timer, e nenhum devolve Permitir/Negar. Por isso o prompt
  > nativo passou a ser o principal.
- Fallback: PSADT (`Show-ADTInstallationPrompt`, botões Permitir/Negar,
  `-NoExitOnTimeout`, `-DefaultButton` alinhado ao `timeoutAction`); a mensagem
  passa a informar a ação padrão e o tempo total, já que o diálogo não tem timer.
- Se ambos falharem, **nega** (fail closed).
- Decisão enviada por HTTP com `SetAgentAuthHeadersWithAgentID`; falha de reporte
  impede o início da sessão.
- **Worker não é relançado após saída intencional**: quando o start é recusado
  (consentimento negado/timeout, falha de reporte) o worker imprime um marcador
  (`[remote-session-worker] exit-ok`) e encerra; o watchdog do serviço NÃO
  relança nesse caso (antes ele interpretava a saída como crash, replayava o
  start e o prompt de consentimento reaparecia em loop).
- **Replay do worker (crash/logoff) consulta o status no servidor**: o payload do
  relançamento leva `respawn=true`; o worker faz
  `GET /agent-auth/me/remote-sessions/{id}` e decide sem reabrir o prompt —
  sessão `active` prossegue (consentimento já autorizado), `pending_consent`
  mostra o prompt, `denied/closed/expired` recusa o start sem prompt. Se a
  consulta falhar, segue o fluxo normal (prompt), validado pelo servidor.
- O servidor também envia `RemoteSessionStop` ao agente quando a decisão é
  negada (best-effort), encerrando qualquer worker/prompt pendente.

## Tema (claro/escuro + accent)
- O worker da sessão remota roda como **SYSTEM** na sessão interativa: `HKCU` é o
  perfil do SYSTEM, não o do usuário. O tema é lido de
  `HKEY_USERS\<SID>\Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`
  (`AppsUseLightTheme`) e o destaque de `HKEY_USERS\<SID>\Software\Microsoft\Windows\DWM`
  (`AccentColor` ABGR / `ColorizationColor` ARGB), escolhendo o SID do
  `explorer.exe` **da sessão atual**. Sem shell/perfil, cai para HKCU e depois no
  tema claro default (`app/core/winui`).

## Banner (informativo, click-through)
- Cartão flutuante topmost no topo do monitor compartilhado: operador + tempo +
  dica "Para encerrar, clique no ícone do agente na bandeja do sistema", no tema
  do Windows.
- **100% click-through** (`WS_EX_LAYERED | WS_EX_TRANSPARENT` + `HTTRANSPARENT`
  no `WM_NCHITTEST`): o banner **nunca captura o mouse**. Como ele é excluído da
  captura, o operador vê o app por baixo e clica "através" dele — sem isso,
  clicar em abas do navegador (topo da tela) não funcionava.
- Não tem mais botão: a desconexão mudou para o ícone da bandeja (abaixo).
- `SetWindowDisplayAffinity(WDA_EXCLUDEFROMCAPTURE)`: NÃO aparece no viewer nem na
  gravação. Se a API falhar, o banner não é exibido (evita vazar no stream).

## Ícone da bandeja (desconectar / avisos)
- Criado junto com a sessão de tela (quando o banner está habilitado). Tooltip com
  operador + tempo; balloon no início ("Sessão remota iniciada por {operador}").
- Menu de contexto (clique esquerdo ou direito): cabeçalho da sessão e
  **"Desconectar sessão remota"** — habilitado apenas quando
  `banner.allowDisconnect=true`. A ação fecha a sessão local e reporta stop ao
  servidor (`StopByUser(..., "banner-disconnect")`).
- **Mesmo ícone do input lock (KVM)**: quando o viewer bloqueia teclado/mouse o
  tooltip/balloon mudam e o ícone permanece (não aparecem dois ícones). Se a
  sessão não tem banner/tray, o input lock mantém o indicador próprio (fallback).
- Recriado no `TaskbarCreated` (reinício do explorer) e removido ao fim da sessão.

## Arquivos
- `app/core/winui` — tema do usuário + desenho GDI compartilhado (cartão, botões,
  texto, barra) com double buffering.
- `app/remote_consent_native_windows.go` — prompt principal com contagem/ação padrão.
- `app/remote_consent_prompt.go` — ordem dos backends (nativo → PSADT → nega),
  textos da ação padrão/contagem e reporte.
- `app/core/screen/session_banner_windows.go` — banner temático click-through.
- `app/core/screen/session_tray_windows.go` — ícone da bandeja (avisos +
  Desconectar) reaproveitado pelo input lock.
- `app/core/remotesession/input_lock.go` — indicador compartilhado com a sessão.
- `app/core/remotesession/consent.go`, `consent_gate.go`
- `app/remote_session_worker.go` — wiring.
