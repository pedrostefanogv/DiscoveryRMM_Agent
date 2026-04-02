# Plano de Execucao - Otimizacao e Melhoria da Integracao com PSADT

Data: 2026-04-01  
Escopo: Discovery Agent (Windows)  
Status: Em execucao (onda tecnica inicial)

## 1. Objetivo

Evoluir a integracao atual do Discovery Agent com PSAppDeployToolkit (PSADT), tornando-a:
- orientada por policy remota (API),
- aderente ao modelo de execucao e UI do PSADT 4.1.x,
- observavel, testavel e segura para rollout gradual.

## 2. Diagnostico Atual (baseline do projeto)

### 2.1 Pontos fortes existentes
- Tipagem e defaults de configuracao PSADT no agent: `app/agent_config.go`.
- Bootstrap de modulo PSADT com persistencia de historico: `app/agent_config.go`, `internal/database/sqlite.go`.
- Executor PSADT funcional com fallback para Winget/Chocolatey: `internal/automation/executor_psadt.go`.
- Notification Center com modos `silent`, `notify_only`, `require_confirmation`: `app/notification_center.go`.
- Persistencia de eventos de notificacao: `internal/database/sqlite.go`.

### 2.2 Gaps principais
- Campos de policy PSADT ja parseados (timeout, fallback, exit codes) ainda nao governam totalmente a execucao real.
- Classificacao de exit code hoje e majoritariamente fixa/hardcoded no executor.
- `notificationPolicies` e `notificationBranding` nao sao aplicados de forma completa por `eventType` no runtime.
- Frontend ainda usa fluxo simplificado para notificacoes (`confirm/alert`) e nao um renderer completo por layout/policy.
- `requiredVersion` no bootstrap precisa garantir semantica de versao minima (>=), nao apenas igualdade estrita.
- `installSource` ainda sem estrategia multipla operacional completa (PSGallery/interno/offline).

## 3. Estrategia de Entrega

Entregar em duas trilhas complementares:

1. **Curto prazo (hardening do fluxo atual)**  
   Fortalecer o executor atual (PSADT como orquestrador de processos) com policy engine completa.

2. **Medio prazo (alinhamento pleno ao modelo PSADT)**  
   Evoluir para fluxo de deployment mais proximo da arquitetura oficial do `Invoke-AppDeployToolkit` (fases e sessao).

## 4. Fases de Execucao

## Fase 0 - Alinhamento e congelamento de contrato
Objetivo: fechar regras antes de codar.

Atividades:
- Fechar semantica de `fallbackPolicy`, `timeoutAction` e `unknownExitCodePolicy`.
- Fechar regra de versao minima (`requiredVersion`) e comportamento de `installOnStartup`/`installOnDemand`.
- Validar matriz `mode x severity x layout x actions` para notificacoes.

Entregaveis:
- Contrato consolidado API-Agent para bloco `psadt`, `notificationPolicies`, `notificationBranding`, `rollout`.

Criterios de aceite:
- Contratos aprovados por Agent/API/Frontend sem ambiguidades.

Criterio de saida (gate):
- Schema versionado e publicado.
- Matriz de comportamento validada em revisao tecnica unica.
- Nenhum campo em aberto sem default documentado.

---

## Fase 1 - Policy Engine PSADT no backend
Objetivo: policy remota governar execucao real.

Atividades:
- Criar resolvedor de policy efetiva por execucao (defaults + config remota).
- Aplicar no runtime:
  - `executionTimeoutSeconds`,
  - `successExitCodes`,
  - `rebootExitCodes`,
  - `ignoreExitCodes`,
  - `timeoutAction`,
  - `unknownExitCodePolicy`,
  - `fallbackPolicy`.
- Normalizar classificacao e metadata da execucao (categoria, fallback aplicado, motivo).

Entregaveis:
- Policy engine aplicada no caminho de automacao PSADT.
- Logs e metadata de execucao com decisao explicita.

Criterios de aceite:
- Mudando policy na API, o comportamento do executor muda sem alteracao de codigo.

Criterio de saida (gate):
- 100% dos cenarios de policy cobertos por teste unitario.
- Classificacao de resultado persistida com motivo explicito (success/reboot/ignore/recoverable/fatal/unknown).
- Sem regressao no caminho Winget/Chocolatey em suite de regressao rapida.

---

## Fase 2 - Executor PSADT aderente ao 4.1.x
Objetivo: aproximar fluxo do modelo oficial PSADT.

Atividades:
- Revisar script de execucao para sessao explicita (`Open-ADTSession`) quando aplicavel.
- Padronizar uso de parametros recomendados no processo (timeout, wait child, pass-thru, codigos de sucesso/reboot/ignore).
- Melhorar mapeamento de falhas reservadas PSADT (faixas 60000-68999, 69000-69999, 70000-79999).
- Preparar caminho para MSI com cmdlets especificos quando o artefato for MSI.

Entregaveis:
- Executor robusto com classificacao de saida alinhada a policy + PSADT.
- Menor dependencia de regras fixas.

Criterios de aceite:
- Cenarios de sucesso, reboot, recoverable, fatal e unknown passam em testes automatizados.

Criterio de saida (gate):
- Suite de integracao com taxa de sucesso >= 95% em ambiente controlado.
- Mapeamento de faixas reservadas PSADT coberto por testes de classificacao.
- Tempo maximo de execucao respeitando `executionTimeoutSeconds` em 100% dos testes de timeout.

---

## Fase 3 - Notification Center por policy (UI + backend)
Objetivo: integrar de ponta a ponta `notificationPolicies` e `notificationBranding`.

Atividades:
- Backend:
  - resolver policy por `eventType`,
  - aplicar allow/block list e rollout,
  - enriquecer payload de `notification:new` com layout/mode/style/actions/branding.
- Frontend:
  - renderer completo para `toast`, `banner`, `modal`,
  - suporte a acoes e retorno de decisao para `RespondToNotification`,
  - comportamento consistente em headless/UI indisponivel.
- Opcional controlado:
  - suporte a formatacao rica de texto (`[bold]`, `[accent]`, `[url]`) com sanitizacao.

Entregaveis:
- Pipeline de notificacao plenamente dirigido por policy remota.
- UX consistente para `silent`, `notify_only`, `require_confirmation`.

Criterios de aceite:
- Mesma notificacao muda visual/comportamento apenas alterando policy na API.

Criterio de saida (gate):
- Cobertura de modos `silent`, `notify_only`, `require_confirmation` em UI e headless.
- Todas as acoes de notificacao retornam decisao rastreavel no backend.
- Sem uso de `alert/confirm` no fluxo principal de notificacao.

---

## Fase 4 - Bootstrap e fontes de instalacao do modulo
Objetivo: tornar bootstrap previsivel e controlado por policy.

Atividades:
- Aplicar `installOnStartup` e `installOnDemand` de forma explicita no fluxo.
- Ajustar validacao de versao para minimo requerido.
- Implementar estrategia de `installSource` (PSGallery e extensao para fonte interna/offline).
- Melhorar logs de origem, escopo (AllUsers/CurrentUser), e erro.

Entregaveis:
- Bootstrap deterministico e auditavel.
- Menos friccao em ambientes restritos.

Criterios de aceite:
- Maquina limpa e ambiente restrito com comportamento esperado e rastreavel.

Criterio de saida (gate):
- `requiredVersion` validado como versao minima (>=) com testes positivos e negativos.
- Estrategia de `installSource` com fallback deterministico e log de origem aplicado.
- Evidencia de execucao em ambiente sem acesso externo (cenario restrito/offline controlado).

---

## Fase 5 - Testes, observabilidade e rollout seguro
Objetivo: reduzir risco operacional.

Atividades:
- Testes unitarios:
  - parse e defaults de config,
  - policy engine e classificacao de exit code.
- Testes de integracao:
  - fluxo PSADT + fallback,
  - notificacoes por modo/layout.
- Observabilidade:
  - eventos recomendados (`install_module_attempt`, `install_module_result`, `notification_dispatched`, `user_confirmation_result`),
  - correlacao por command/execution/notification id.
- Rollout progressivo por flags.

Entregaveis:
- Suite minima automatizada.
- Checklist de troubleshooting operacional.

Criterios de aceite:
- Regressao controlada e rollback funcional por flags/policy.

Criterio de saida (gate):
- Runbook de rollback testado em pelo menos 1 simulacao.
- Indicadores minimos de observabilidade publicados (erros, fallback, timeout, confirmacao).
- Plano de rollout por ondas aprovado com criterio explicito de promocao e bloqueio.

## 5. Matriz resumida de decisao de falha

| Condicao detectada | Classificacao | Acao primaria | Acao secundaria |
|---|---|---|---|
| Timeout de execucao | recoverable/fatal (por policy) | Aplicar `timeoutAction` | Avaliar fallback por `fallbackPolicy` |
| Exit code em `successExitCodes` | success | Encerrar sem fallback | Registrar metrica de sucesso |
| Exit code em `rebootExitCodes` | reboot_required | Encerrar com sinalizacao de reboot | Registrar necessidade de reboot |
| Exit code em `ignoreExitCodes` | ignored | Encerrar como ignorado | Persistir motivo de ignore |
| Exit code desconhecido | `unknownExitCodePolicy` | Classificar por policy | Fallback somente se permitido |
| Falha de modulo ausente/import | recoverable | Tentar bootstrap/on-demand | Fallback para Winget/Chocolatey |

## 6. Governanca de rollout e rollback

Rollout por ondas:
1. Onda 1 (canary): 5% dos agentes elegiveis.
2. Onda 2 (parcial): 25% dos agentes elegiveis.
3. Onda 3 (ampliada): 50% dos agentes elegiveis.
4. Onda 4 (geral): 100% dos agentes elegiveis.

Criterios de promocao entre ondas:
- Taxa de falha fatal abaixo de 2%.
- Timeout abaixo de 3%.
- Regressao Winget/Chocolatey abaixo de 1%.
- Sem incidente de seguranca relacionado ao fluxo.

Criterios de bloqueio/rollback:
- Falha fatal >= 2% em janela de observacao.
- Perda de rastreabilidade de eventos criticos.
- Erro sistemico em confirmacao de notificacao obrigatoria.

Rollback operacional:
- Desabilitar caminho PSADT por policy remota (`psadt.enabled=false`) quando necessario.
- Forcar fallback para caminho legado (Winget/Chocolatey) por flags.
- Registrar incidente, causa raiz inicial e janela de recuperacao.

## 7. Ordem Recomendada de Implementacao

1. Fase 0  
2. Fase 1  
3. Fase 2  
4. Fase 3  
5. Fase 4  
6. Fase 5

## 8. Indicadores minimos de observabilidade

- Taxa de sucesso por tipo de instalacao (PSADT/Winget/Chocolatey).
- Taxa de fallback (por motivo e por versao do agente).
- Taxa de timeout por policy efetiva.
- Distribuicao de `user_confirmation_result` por `eventType`.
- Tempo medio de execucao por tipo de artefato.

## 9. Riscos e Mitigacoes

- Restricoes de permissao/ExecutionPolicy:
  - Mitigar com mensagens claras e fallback de escopo.
- Divergencia API x runtime (exit codes/policies):
  - Mitigar com policy engine unica e testes de contrato.
- Ambiente sem UI ativa:
  - Mitigar com regras headless explicitas e persistencia de evento.
- Personalizacao visual invalida:
  - Mitigar com validacao de payload e fallback de tema/layout.
- Rollout sem gate objetivo:
  - Mitigar com criterios de promocao e rollback automatizavel por policy.

## 10. Referencias utilizadas

### Documentacao PSAppDeployToolkit (consultada)
- https://psappdeploytoolkit.com/docs/4.1.x/reference/config-settings
- https://psappdeploytoolkit.com/docs/4.1.x/reference/text-formatting
- https://psappdeploytoolkit.com/docs/4.1.x/reference/variables
- https://psappdeploytoolkit.com/docs/4.1.x/reference/adtsession-object
- https://psappdeploytoolkit.com/docs/4.1.x/deployment-concepts/invoke-appdeploytoolkit
- https://psappdeploytoolkit.com/docs/4.1.x/usage/adding-ui-elements
- https://psappdeploytoolkit.com/docs/4.1.x/usage/installing-applications
- https://psappdeploytoolkit.com/docs/4.1.x/reference/exit-codes
- https://psappdeploytoolkit.com/docs/4.1.x/reference/functions/Start-ADTProcess
- https://psappdeploytoolkit.com/docs/4.1.x/reference/functions/Show-ADTInstallationWelcome

### Base de codigo analisada
- `app/agent_config.go`
- `app/notification_center.go`
- `app/psadt_debug_bridge.go`
- `internal/automation/executor_psadt.go`
- `internal/automation/executor.go`
- `internal/automation/service.go`
- `internal/database/sqlite.go`
- `app/sync.go`
- `frontend/app.js`
- `frontend/js/app-init.js`
- `DOCs/GUIA_INTEGRACAO_PSADT_API_OPERACAO.md`
- `DOCs/PLANO_INTEGRACAO_PSADT_NOTIFICACOES.md`

## 11. Progresso de implementacao (snapshot)

Data de atualizacao: 2026-04-02

Concluido nesta onda:
- Policy PSADT aplicada em runtime no executor de automacao (`executionTimeoutSeconds`, `successExitCodes`, `rebootExitCodes`, `ignoreExitCodes`, `fallbackPolicy`, `timeoutAction`, `unknownExitCodePolicy`).
- Resolucao de policy conectada entre App e Automation Service (resolver central).
- Semantica de versao minima (>=) no bootstrap de modulo PSADT.
- Integracao inicial de notificacao de execucao no fluxo de automacao (`install_start`, `install_end`, `install_failed`) com correlacao por `executionId`.
- Enriquecimento de metadata de execucao com policy PSADT efetiva.
- Fluxo inicial de deferimento implementado (`deferred`) no ciclo de confirmacao de notificacao (frontend + backend + automacao).
- Reexecucao automatica apos deferimento com intervalo padrao em memoria (`defaultDeferInterval`), com limite padrao de adiamentos (`defaultDeferTimes`).
- Testes unitarios adicionados para fluxo de notificacao e decisao de deferimento no pacote de automacao.
- Persistencia duravel de deferimento em banco (`automation_defer_state`) com carga de estado no startup, atualizacao em cada defer e status final auditavel ao concluir execucao.
- Aplicacao de `notificationPolicies` por `eventType` no Notification Center com precedencia sobre payload de dispatch e enriquecimento com `actions`, `styleOverride` e `branding`.
- Caminho MSI dedicado no executor PSADT (`Start-ADTMsiProcess`) para pacotes `.msi`/`msi:`.
- Estrategia multipla de `installSource` no bootstrap (`powershell_gallery`, `internal:<repo>`, `offline:<path>`) com fallback deterministico para PSGallery.
- Testes de integracao adicionados para ciclo de deferimento com persistencia e recarga de estado.

Em andamento:
- Expandir renderer frontend para consumir integralmente `actions/styleOverride/branding` enviados por policy no evento `notification:new`.
- Acrescentar cenarios de rollback automatizavel por policy em testes de regressao.

Pendencias criticas:
- Cobrir fluxo end-to-end de notificacao com renderer frontend completo e retorno de acoes customizadas.
- Consolidar enforcement de `CloseProcesses`/`BlockExecution`/`CheckDiskSpace` no executor com acao operacional.

## 12. Backlog operacional de tarefas

Prioridade P0:
1. [x] Fechar behavior de notificacao por `eventType` no backend (`notificationPolicies` como fonte principal).
2. [x] Criar testes de integracao para ciclo de deferimento e recarga de estado persistido.
3. [x] Persistir e consultar estado de deferimento em auditoria de execucao.

Prioridade P1:
1. [x] Implementar suporte MSI dedicado no caminho PSADT.
2. [x] Implementar estrategia de `installSource` em modo restrito/offline.
3. [ ] Acrescentar cenarios de rollback automatizavel por policy em testes de regressao.

Prioridade P2:
1. [~] Expandir renderer de notificacao no frontend com politica completa de layout/acoes.
2. [ ] Criar painel de observabilidade para taxa de fallback, timeout e falha fatal por onda.

Checklist de pronto por fase (controle):
- [x] Fase 1 (core policy runtime) - implementacao inicial concluida.
- [~] Fase 2 (aderencia executor PSADT 4.1.x) - parcial avancada (caminho MSI incluido).
- [~] Fase 3 (notification center por policy) - parcial avancada (policy por eventType aplicada no backend).
- [~] Fase 4 (bootstrap e fontes) - parcial avancada (`requiredVersion>=` e `installSource` multipla implementados).
- [~] Fase 5 (testes e rollout seguro) - parcial avancada (novos testes de integracao de deferimento).

## 13. Estrategia de deferimento (adiar) baseada no PSADT

Objetivo:
- Permitir adiar quando policy permitir.
- Reexecutar automaticamente a acao ate conclusao real.
- Bloquear opcao de adiar ao atingir limite (vezes ou prazo), seguindo o comportamento do PSADT.

Base oficial PSADT 4.1.x (Show-ADTInstallationWelcome):
- `AllowDefer` habilita botao de adiar.
- `AllowDeferCloseProcesses` permite adiar apenas quando ha processos alvo abertos.
- `DeferTimes` limita numero de adiamentos.
- `DeferDays` define janela em dias desde a primeira exibicao (convertida em deadline).
- `DeferDeadline` define prazo absoluto de adiamento.
- `DeferRunInterval` evita re-prompt imediato e define quando voltar a solicitar.
- `CloseProcessesCountdown` aplica contagem para forcar progresso quando deferimento expirou.
- `ForceCloseProcessesCountdown` aplica contagem mesmo com deferimento disponivel.

Regra funcional proposta para Discovery:
1. Se `allowDefer=true` e limite nao expirou, usuario pode escolher `deferred`.
2. Ao receber `deferred`, sistema persiste estado e agenda nova tentativa em `nextAttemptAt`.
3. Em cada nova tentativa, recalcula elegibilidade de deferimento:
  - bloqueia se `deferCount >= DeferTimes`;
  - bloqueia se `now >= DeferDeadline`;
  - bloqueia se `now >= firstDeferAt + DeferDays`.
4. Quando bloqueado, UI nao oferece adiar e fluxo segue para execucao obrigatoria/controlada por countdown/policy.
5. Fluxo termina apenas com resultado final (`approved` + execucao concluida) ou falha operacional tratada por policy.

Modelo de estado minimo para persistencia:
- `executionId`
- `taskId`
- `deferCount`
- `firstDeferAt`
- `lastDeferAt`
- `deadlineAt`
- `nextAttemptAt`
- `deferExhausted` (bool)
- `finalStatus` (`completed`, `failed`, `cancelled`)

Eventos de observabilidade adicionais:
- `user_defer_selected`
- `user_defer_exhausted`
- `execution_rescheduled_after_defer`
- `execution_forced_after_defer_limit`

Regras de agendamento (reexecucao):
- `nextAttemptAt = now + DeferRunInterval` quando houver deferimento.
- Se `DeferRunInterval` ausente, aplicar default conservador (ex.: 30 minutos).
- Se tentativa ocorrer antes de `nextAttemptAt`, nao exibir prompt novamente.
- Reentrada deve preservar idempotencia por `executionId`/`idempotencyKey`.

Backlog tecnico para suportar deferimento fim-a-fim:
1. Backend Notification Center:
  - adicionar resultado `deferred` em `RespondToNotification`.
  - persistir metadados de deferimento.
2. Frontend:
  - incluir acao de adiar quando policy habilitar.
  - ocultar acao de adiar quando `deferExhausted=true`.
3. Automation Service:
  - criar fila de reexecucao por `nextAttemptAt`.
  - remarcar tarefa ao receber `deferred`.
4. Database:
  - tabela/colunas para estado de deferimento por execucao/tarefa.
5. Testes:
  - cenario com `DeferTimes` ate esgotar e bloqueio de adiar.
  - cenario com `DeferDeadline` expirado.
  - cenario com `DeferRunInterval` (nao reprompt imediato).

Criterio de aceite especifico (deferimento):
- Usuario pode adiar dentro dos limites definidos.
- Sistema reexecuta automaticamente apos intervalo configurado.
- Ao atingir limite, opcao de adiar some e fluxo segue para conclusao obrigatoria.
- Toda decisao de deferimento fica auditavel com timeline completa.

## 14. Plano de implementacao - Show-ADTInstallationWelcome (tarefas)

Status: Em execucao (atualizado em 2026-04-02)

Tarefas P0:
1. [x] Implementar parser de opcoes Welcome (`allowDefer`, `deferTimes`, `deferDays`, `deferDeadline`, `deferRunIntervalSeconds`) a partir de payload/metadata da tarefa.
2. [x] Aplicar enforcement de deferimento por limite de vezes e limite temporal no runtime de automacao.
3. [x] Persistir e propagar no metadata de notificacao o estado de deferimento (`deferCount`, `deferRemaining`, `deferExhausted`, `nextAttemptAt`, `deferDeadlineAt`).
4. [x] Classificar saida PSADT com reboot requerido (`3010`, `1641`) como evento `reboot_required` para UX dedicada.
5. [x] Cobrir regras novas com testes automatizados (parser Welcome, deadline expirado, evento reboot).

Tarefas P1:
1. [~] Forcar comportamento operacional de `CloseProcesses` e `AllowDeferCloseProcesses` no executor (alem da exposicao de metadata para UI).
2. [~] Implementar enforcement de `BlockExecution` e `CheckDiskSpace` antes da instalacao.
3. [ ] Integrar countdown de fechamento forcado com acao real no host quando `CloseProcessesCountdown`/`ForceCloseProcessesCountdown` expirar.

Arquivos implementados nesta iteracao:
- `internal/automation/service.go`
- `internal/automation/service_notifications_test.go`
- `internal/automation/service_deferred_integration_test.go`

## 15. Plano de implementacao - Show-ADTDialogBox (tarefas)

Status: Concluido (2026-04-02)

Tarefas executadas:
1. [x] Adicionar suporte nativo ao tipo `dialog_box` no bridge PSADT com chamada real de `Show-ADTDialogBox`.
2. [x] Expor opcoes de `Buttons`, `DefaultButton`, `Icon` e `Timeout` para testes de UX operacional.
3. [x] Expor e mapear switches `NoWait`, `ExitOnTimeout`, `NotTopMost` e `Force`.
4. [x] Ajustar timeout de execucao do script conforme timeout de dialogo para evitar falso timeout no host.
5. [x] Criar cobertura de teste para validacao do script gerado e normalizacao de parametros.

Arquivos atualizados nesta onda de DialogBox:
- `app/psadt_debug_bridge.go`
- `frontend/js/psadt-debug.js`
- `frontend/partials/views/psadtView.html`
- `frontend/psadt-debug.html`
- `app/psadt_visual_notification_test.go`
