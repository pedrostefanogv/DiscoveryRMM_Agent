# Checklist Manual — Fase E.2 (Validação em Máquina Real)

> **Revisado em 2026-10-17** (auditoria de arquitetura). Mudanças em relação à versão antiga:
> - Bloco de "fallback standalone" **removido**: o fallback foi removido (M5) — a UI é sempre
>   interface e aguarda o serviço; o log é `modo interface aguardando serviço (M5: sem fallback standalone)`.
> - Log principal é `logs.db`; `agent-service.log` existe apenas como fallback de texto.
> - O `taskkill /IM discovery-service.exe` **roda** no update in-place (após `sc stop`) — antes dizia o contrário.
> - Novos blocos: segurança do IPC (token/canais), `config:set` + watcher, rollback do
>   `discovery-service.exe`, multiusuário/RDP e preshutdown (Fase 5/§8.3).
>
> Plano e escopo: `PLANO_AGENT_ARQUITETURA_E_HERANCA_DEPARTAMENTO.md` (fase A.5).

Cada bloco deve ser executado numa VM Windows 10/11 **com snapshot limpo**.
Instale via NSIS (`task build:windows:installer`) com os **dois binários**
(`discovery-agent.exe` + `discovery-service.exe`). Marque `[x]` e registre evidência (⚑ = screenshot/log).

## Pré-requisitos

- [ ] VM Windows 10/11 atualizada; snapshot limpo criado **antes** da instalação.
- [ ] Build atual: `task build:windows:installer` sem erros; instalador contém os dois binários em `%ProgramFiles%\Discovery\`.
- [ ] Servidor RMM acessível (tenant/site de teste) e credenciais de provisionamento válidas.
- [ ] PowerShell como admin para `sc query`, `Get-Service`, `handle`/Process Explorer e WTS.
- [ ] `C:\ProgramData\Discovery\logs\logs.db` acessível (SQLite) para evidências.

## 1. Instalação limpa + serviço

- [ ] Instalador conclui sem erro; `sc qc DiscoveryAgent` → `BINARY_PATH_NAME` aponta para `discovery-service.exe` (sem `--service`); `sc query DiscoveryAgent` → `RUNNING`.
- [ ] ⚑ `logs.db`: linhas do serviço com `[service] core ativo` e as fases de startup (inventory/agentConn/automation/P2P/self-update).
- [ ] UI `discovery-agent.exe` abre **sem UAC** (usuário padrão), tray aparece e a página de status mostra companion conectado.
- [ ] ⚑ Log da UI: `[ipc] conectado ao serviço` + `modo interface` (não existe mais fallback standalone).
- [ ] Nenhum `.db` aberto pela UI (decisão D3): ⚑ Process Explorer/handle64 → `discovery-agent.exe` sem handle de `logs.db`.

## 2. Boot sem login (sessão 0)

- [ ] Reiniciar a VM e **não** logar: após ~2 min, `sc query DiscoveryAgent` → RUNNING.
- [ ] ⚑ `logs.db` mostra heartbeat enviado antes de qualquer login.
- [ ] Dashboard mostra o agente **online antes** do login.
- [ ] Após login: UI companion conecta; agente continua com a mesma identidade (sem duplicar).
- [ ] `uiOnline=true` no heartbeat após a UI conectar (⚑ campo na API/evento do dashboard).

## 3. Segurança do IPC (novo — S1/S2/S3)

- [ ] Pipe de leitura `\\.\pipe\discovery-agent-ipc` aceita conexão de usuário padrão; **método mutante nele é recusado**.
- [ ] Pipe admin `\\.\pipe\discovery-agent-ipc-admin` exige token de sessão; `debug:set`/ `config:set` sem token → `ipc_unauthorized` (⚑ log do serviço com `sid`/`pid`/`session`).
- [ ] ⚑ Tentar `debug:set` com um script PowerShell (usuário padrão, sem a UI) → recusado; config do serviço **inalterada**.
- [ ] Mensagem `remote_session` forjada (script, sem token) → recusada; **nenhum** worker spawnado (⚑ ausência de processo `--remote-session-worker`).
- [ ] `notification:respond` de outra sessão/usuário → recusado (só o destinatário responde).
- [ ] Rate limit: rajada de RPCs (script) → conexão limitada/respondida com erro, serviço estável.
- [ ] `hello_ack` inclui `version` e `token` (⚑ captura do handshake ou log).

## 4. `config:set` e watcher (novo)

- [ ] Alterar `apiServer`/modo debug na UI → RPC `config:set` aplica **sem reiniciar** o serviço (⚑ log).
- [ ] ⚑ `debug_config.json` não fica truncado após 10 salvamentos seguidos (validar JSON e DACL `SYSTEM:F` + `Users:R`).
- [ ] Editar `debug_config.json` manualmente (admin) com um campo válido → serviço recarrega em ≤3 s (⚑ log do watcher).
- [ ] Gravar JSON inválido no arquivo → serviço **mantém a config anterior** e loga (não cai nem perde config).
- [ ] Com o serviço parado, a UI **não** grava o arquivo local (erro explícito na UI).

## 5. uiOnline, notificações e toast (C2/D4)

- [ ] Com UI aberta: `uiOnline: true`; fechar a UI → próximo heartbeat `false`.
- [ ] `notify_only` com UI aberta → aparece na UI (sem toast duplicado).
- [ ] `notify_only` sem UI → toast nativo no Action Center (⚑ screenshot).
- [ ] `require_confirmation` sem UI → toast informativo + resultado `timeout_policy_applied` no backend.
- [ ] `require_confirmation` com UI → Aprovar chega como `approved`; Adiar como `deferred`.
- [ ] Multiusuário: duas sessões (console + RDP) com UI → notificação chega à sessão **destinatária**; `uiOnline` reflete as duas.

## 6. RPCs via IPC (Fase C)

- [ ] Memory Notes: criar/listar/remover na UI → persistido no DB do serviço (⚑ `[ipc-rpc]`).
- [ ] Contadores de pendências (RPC `status:pending_counts`) batem com o serviço.
- [ ] Página de atualizações: scan roda no **serviço** (⚑ log `[winget upgrade]`); resultado na UI.
- [ ] Automação: estado da UI (`PolicyFingerprint`/`TaskCount`) bate com o log do serviço.
- [ ] Logs da UI mostram linhas do serviço (`logs:tail`).
- [ ] Handshake versionado: forçar versão/protocolo diferente na UI → cai para erro claro (sem crash) e loga incompatibilidade.

## 7. Multiusuário / RDP (novo)

- [ ] Usuário padrão (não-admin) abre a UI e opera: status, notas, chamados, updates (sem UAC).
- [ ] Duas sessões simultâneas com UI: ambas conectam (tokens distintos), sem derrubar a outra.
- [ ] Sair do console (logoff) com UI aberta em RDP: a sessão RDP continua funcionando.
- [ ] Watchdog: matar a UI na sessão RDP → ela é relançada **naquela sessão**; na sessão do console idem (⚑ WTS).

## 8. Atualização in-place e rollback (D2/A.2)

- [ ] Publicar versão nova no backend; self-update do serviço: ⚑ log `[update]` com download + troca e serviço voltando RUNNING.
- [ ] Update com UI aberta: UI reconecta após o serviço subir (token novo do handshake) e mostra a versão nova.
- [ ] `sc query` durante o update: serviço para e volta (⚑ sem janela de "parado" > 2 min).
- [ ] **Rollback**: forçar falha no start do serviço após troca (ex.: renomear binário) → instalador restaura `.bak_update` e o serviço volta RUNNING (⚑ log de rollback).
- [ ] Versão divergente UI×serviço: substituir apenas o `discovery-agent.exe` por versão diferente → aviso claro/erro tratado (não silencioso).

## 9. Preshutdown / reboot (Fase 5 / §8.3)

- [ ] Com usuário logado: reiniciar pelo menu → ⚑ `logs.db` mostra PRESHUTDOWN recebido e teardown gracioso (não os 4 s de fallback).
- [ ] Sem usuário logado: idem (⚑ mesmo bloco de log).
- [ ] `sc stop DiscoveryAgent` → para em < 5 s.
- [ ] Windows Update (ou `shutdown /r /t 0`) → agente reinicia e volta online sem intervenção.

## 10. Inventário SYSTEM vs usuário

- [ ] Inventário completo chega ao site com agente em modo serviço (coleta SYSTEM ok).
- [ ] Winget na sessão 0: catálogo/updates funcionam via serviço (⚑ sem erro de contexto de sessão).
- [ ] Impressoras/inventário de usuário (quando aplicável) funciona na UI.

## 11. Encerramento e desinstalação

- [ ] Desinstalar pelo painel: serviço parado/removido (`sc query DiscoveryAgent` inexistente); taskkill cobre `discovery-service.exe` no uninstall.
- [ ] `%ProgramData%\Discovery` preservado conforme política; nenhum processo órfão.
- [ ] Task de logon `DiscoveryAgentUI` removida no uninstall.

## Coleta de evidências (anexar ao relatório)

```powershell
# Versões e serviço
sc qc DiscoveryAgent; sc query DiscoveryAgent
(Get-Item "$env:ProgramFiles\Discovery\discovery-agent.exe").VersionInfo.FileVersion
(Get-Item "$env:ProgramFiles\Discovery\discovery-service.exe").VersionInfo.FileVersion

# Handles do SQLite por processo (Process Explorer ou handle64)
# handle64.exe -p discovery-agent.exe | Select-String "\.db"

# Últimas linhas do log do serviço (SQLite)
# sqlite3 C:\ProgramData\Discovery\logs\logs.db "select * from logs order by id desc limit 200;"

# Sessões ativas (multiusuário)
query session

# Teste de recusa do IPC (usuário padrão, sem a UI)
# .\ipc-probe.ps1  (script de teste que tenta debug:set no pipe admin sem token)
```

## Critérios de aceite (resumo)

1. Nenhum `SQLITE_BUSY`/conflito de DB entre UI e serviço (D3) em nenhuma etapa.
2. `uiOnline` correto em UI on/off e com 2+ sessões.
3. Notificações nos 4 caminhos (UI on, UI off, require_confirmation, timeout) e na sessão correta.
4. Update in-place com serviço ativo e UI aberta; **rollback** funciona quando o start falha.
5. Nenhum método mutante do IPC responde sem token do chamador; `remote_session` forjado é recusado.
6. `debug_config.json` nunca corrompido; alteração externa aplicada sem restart.
7. Preshutdown entregue antes do logoff; `sc stop` < 5 s.
8. Usuário padrão (sem UAC) opera a UI; duas sessões simultâneas funcionam.
