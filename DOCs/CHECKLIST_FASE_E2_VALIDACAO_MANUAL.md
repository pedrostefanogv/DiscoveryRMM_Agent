# Checklist Manual — Fase E.2 (Validação em Máquina Real)

> PLANO_SEPARACAO_SERVICO_UI.md — separação serviço (core) × UI.
> Cada bloco de `☐` deve ser executado numa máquina Windows de teste (VM snapshot limpo recomendado).
> Instale via NSIS (`build:windows:installer`) com os **dois binários** (`discovery-agent.exe` + `discovery-service.exe`).
> Marque `[x]` conforme executa; registre evidências (screenshot/log) nos itens ⚑.

## Pré-requisitos

- [ ] VM Windows 10/11 atualizada, snapshot limpo criado antes da instalação.
- [ ] Build atual: `task build:windows:installer` executado sem erros; instalador contém os 2 binários (verificar `%ProgramFiles%\Discovery\`).
- [ ] Servidor RMM acessível (tenant/site de teste) e credenciais de provisionamento válidas.
- [ ] PowerShell como admin para comandos de verificação (`sc query DiscoveryAgent`, etc.).

## 1. Instalação limpa + serviço

- [ ] Instalador completa sem erro; `sc query DiscoveryAgent` → `RUNNING`; binPath aponta para `discovery-service.exe` (sem `--service`).
- [ ] ⚑ `Get-Content C:\ProgramData\Discovery\logs\agent-service.log -Tail 50` mostra: `[service] core ativo — staged startup iniciando` e fases inventory/agentConn/automation/P2P/self-update.
- [ ] UI `discovery-agent.exe` abre; tray aparece; página de status mostra conectado (companion: serviço ativo).
- [ ] Log da UI (sessão do usuário) mostra `[ipc] conectado ao serviço` e `[startup] serviço DiscoveryAgent ativo — modo companion`.
- [ ] NÃO existe `discovery-agent.db`/SQLite aberto pelo processo da UI (decisão D3): ⚑ `handle64`/Process Explorer → handles do `discovery-agent.exe` não contêm `.db`; somente `discovery-service.exe` tem o handle do SQLite.

## 2. Boot sem login (sessão 0)

- [ ] Reiniciar a VM e NÃO fazer login: após ~2 min, `sc query DiscoveryAgent` → RUNNING e log mostra heartbeat enviado (⚑ trecho do log com `heartbeat`).
- [ ] No site (dashboard), agente aparece online **antes** do login do usuário.
- [ ] Fazer login: UI companion conecta; site continua mostrando o mesmo agente (sem duplicar identidade).
- [ ] Heartbeat do site passa a incluir **uiOnline=true** após a UI conectar (⚑ campo na resposta da API `/agents` ou evento do dashboard).

## 3. uiOnline no heartbeat (C2 + backend)

- [ ] Com UI aberta: `uiOnline: true` no heartbeat (⚑ evidência no dashboard event / API).
- [ ] Fechar a UI (encerrar processo `discovery-agent.exe`): próximo heartbeat traz `uiOnline: false` (aguardar ciclo de heartbeat ~60s).
- [ ] Agente standalone antigo (instalação prévia, sem serviço): heartbeat **sem** o campo `uiOnline` (retrocompatibilidade — backend não deve quebrar).
- [ ] Backend exibe o estado corretamente (novo campo `uiOnline` em `HeartbeatMetricsDto` — verificar resposta de GET do agent).

## 4. Notificações + toast nativo (C2/D4 — sessão 0)

- [ ] Com UI conectada: disparar notificação `notify_only` do backend → aparece **na UI** (via IPC), sem toast nativo duplicado.
- [ ] Fechar a UI: disparar `notify_only` → **toast nativo do Windows** aparece no Action Center (⚑ screenshot). Nota: toast de serviço SYSTEM pode não exibir ações — comportamento aceito (§0.3).
- [ ] Disparar `require_confirmation` com UI fechada: toast informativo (sem ações) + após timeout configurado, resultado `timeout_policy_applied` no log do serviço e no backend.
- [ ] Com UI conectada: `require_confirmation` → modal/toast na UI; clicar **Aprovar** → resposta chega ao backend como `approved` (⚑ log `[ipc] resposta de notificação`); clicar **Adiar** → `deferred`.
- [ ] Timeout com UI aberta sem interação → `timeout_policy_applied`.

## 5. RPCs via IPC (Fase C + §0.5)

- [ ] Memory Notes: criar/listar/remover na UI (companion) → dados persistidos no DB do serviço; ⚑ log `[ipc-rpc]`.
- [ ] Contadores de pendências (Página de status): `PendingCommandResults`/`PendingP2PTelemetry` mostram valores do serviço (RPC `status:pending_counts`).
- [ ] Página de atualizações da UI: em companion, scan roda **no serviço** — ⚑ log do serviço mostra `[winget upgrade]` no momento do clique (RPC `updates:scan`); resultado aparece na UI.
- [ ] Página de automação: estado exibido reflete o motor do serviço (RPC `automation:state`) — ⚑ comparar `PolicyFingerprint`/`TaskCount` da UI com o log do serviço.
- [ ] Logs da UI (página de logs): em companion mostra as linhas do serviço (RPC `logs:tail`) — ⚑ disparar um evento no serviço e ver a linha aparecer na UI de logs.
- [ ] Handshake versionado: log da UI mostra modo companion sem aviso de protocolo; ⚑ (opcional) mudar `IPCProtocolVersion` só na UI e confirmar que ela cai para standalone com log de protocolo incompatível.

## 6. Fallback standalone (companion.Controller)

- [ ] Parar o serviço (`sc stop DiscoveryAgent`) com UI aberta: tray fica offline; UI tenta reconectar (log `[ipc] desconectado`).
- [ ] Após **5 min** de serviço ausente: UI assume core standalone — log `serviço ausente por 5min — assumindo core standalone` + evento `service:companion_lost`; agente reconecta ao site pela UI (⚑ dashboard online com `pid` da UI).
- [ ] Reabrir o serviço (`sc start DiscoveryAgent`): ⚑ validar comportamento documentado (a UI que assumiu standalone continua; nova UI conecta como companion).
- [ ] Desinstalar o serviço com UI aberta → mesmo fallback; sem crash.

## 7. Atualização in-place (D2/self-update)

- [ ] Publicar versão nova do agente no backend; aguardar self-update do serviço: ⚑ log `[update]` mostra download e troca do binário com serviço ativo; `CanInstallNow` true no serviço.
- [ ] `taskkill /fi "imagename eq discovery-service.exe"` não roda durante update (NSIS `PrepareForInPlaceUpdate` encerra corretamente) — ⚑ log do instalador.
- [ ] Pós-update: serviço sobe com novo binário; UI (se aberta) reconecta via IPC; versão nova no dashboard.
- [ ] Update com UI aberta: UI também é atualizada (se aplicável) sem perder o pipe — ⚑ handshake novo (`protocol`) aceito.

## 8. Inventário SYSTEM vs usuário

- [ ] Inventário completo chega ao site com agente em modo serviço (coleta SYSTEM ok).
- [ ] Winget na sessão 0: catálogo/updates funcionam via serviço (D2) — ⚑ sem erro de contexto de sessão no log.
- [ ] Impressoras/inventário de usuário (quando aplicável) funciona na UI standalone.

## 9. Encerramento e desinstalação

- [ ] Desinstalar pelo painel: serviço parado e removido (`sc query DiscoveryAgent` → inexistente); taskkill cobre `discovery-service.exe` no uninstall; ⚑ log do desinstalador.
- [ ] Arquivos removidos (ProgramData/Discovery preservado se política define); UI não deixa processo órfão.

## Critérios de aceite (resumo)

1. Nenhum `SQLITE_BUSY`/conflito de DB entre UI e serviço (D3) em nenhuma etapa.
2. `uiOnline` reflete corretamente presença da UI (C2) nos 3 estados: UI on / UI off / standalone.
3. Notificações funcionam nos 4 caminhos: UI on (render na UI), UI off (toast), require_confirmation (resposta via UI ou timeout), sem crash.
4. Fallback standalone aciona em 5 min e a máquina nunca fica sem agente.
5. Update in-place funciona com serviço ativo e com UI aberta.
