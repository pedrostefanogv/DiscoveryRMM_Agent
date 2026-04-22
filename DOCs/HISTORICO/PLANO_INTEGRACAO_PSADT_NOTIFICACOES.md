# Plano de Integracao PSADT + Notificacoes via API

> Status de consolidacao: historico.
>
> Documento operacional canonico: DOCs/GUIA_INTEGRACAO_PSADT_API_OPERACAO.md
>
> Roadmap de execucao atual: DOCs/PLANO_EXECUCAO_OTIMIZACAO_INTEGRACAO_PSADT.md

Data: 2026-04-01
Escopo: Agent Discovery (Windows)

## 1. Objetivo

Integrar o PSAppDeployToolkit 4.1.8 ao fluxo de automacao do agent e preparar um sistema de notificacoes personalizaveis dirigido por API, com suporte a:

- aviso nao bloqueante ao usuario
- confirmacao obrigatoria quando exigido
- modo silencioso
- personalizacao visual (cores, icone, banner, nome da empresa)

## 2. Decisoes de Produto

1. Bootstrap do modulo PSADT: PowerShell Gallery oficial.
2. Consentimento: arquitetura modular com suporte a tres modos (`silent`, `notify_only`, `require_confirmation`), com inicio em confirmacao obrigatoria para cenarios alvo.
3. Personalizacao: identidade global (icone/banner/nome empresa) + override por tipo de evento (cor/layout/criticidade).
4. Auto-instalacao do modulo: ligada por padrao, com controle por flag na API.

Nota tecnica (Windows + PSADT nativo):

- No canal `Show-ADTBalloonTip`, o nome de origem exibido pelo Windows pode permanecer `PSAppDeployToolkit`.
- O icone e limitado aos valores nativos (`None`, `Info`, `Warning`, `Error`), sem suporte a icone custom.
- Para identidade visual totalmente custom, usar renderer proprio do frontend (toast/banner/modal) em vez do balloon nativo do PSADT.

## 3. Arquitetura Atual Aproveitada

Pontos existentes no projeto que serao reutilizados:

- Ingestao de configuracao remota: `app/agent_config.go` + `app/sync.go` (`/api/agent-auth/me/configuration`)
- Execucao de automacao: `internal/automation/types.go` + `internal/automation/executor.go`
- Roteamento de instalacao P2P/Winget: `app/automation_p2p.go`
- Ciclo de vida e wiring: `app/app.go`
- Persistencia/config local Windows: `app/installer.go`
- UX atual de toasts: `frontend/app.js` + `frontend/styles.css`

## 4. Contrato de API Proposto

### 4.1 Extensao de `/api/agent-auth/me/configuration`

Adicionar 3 blocos novos ao payload:

- `psadt`
- `notificationBranding`
- `notificationPolicies`

Exemplo:

```json
{
  "psadt": {
    "enabled": true,
    "requiredVersion": "4.1.8",
    "autoInstallModule": true,
    "installSource": "powershell_gallery",
    "executionTimeoutSeconds": 1800,
    "fallbackPolicy": "winget_then_choco",
    "installOnStartup": true,
    "installOnDemand": true
  },
  "notificationBranding": {
    "companyName": "Meduza",
    "logoUrl": "https://cdn.exemplo.com/brand/logo.svg",
    "bannerUrl": "https://cdn.exemplo.com/brand/banner.png",
    "theme": {
      "surface": "#0f172a",
      "text": "#f8fafc",
      "accent": "#0284c7",
      "success": "#0b6e4f",
      "warning": "#8a4e12",
      "danger": "#9a031e"
    }
  },
  "notificationPolicies": [
    {
      "eventType": "install_start",
      "mode": "notify_only",
      "severity": "medium",
      "timeoutSeconds": 8,
      "styleOverride": {
        "layout": "toast",
        "background": "#1e293b",
        "text": "#f8fafc"
      }
    },
    {
      "eventType": "install_error",
      "mode": "require_confirmation",
      "severity": "high",
      "timeoutSeconds": null,
      "styleOverride": {
        "layout": "banner",
        "background": "#7f1d1d",
        "text": "#fee2e2"
      },
      "actions": [
        { "id": "retry", "label": "Tentar novamente", "actionType": "retry_install" },
        { "id": "details", "label": "Ver detalhes", "actionType": "open_logs" }
      ]
    }
  ]
}
```

### 4.2 Endpoint de notificacao ativa da API (push de aviso)

Opcao recomendada:

- `POST /api/agent-auth/me/notifications/dispatch`

Payload:

```json
{
  "idempotencyKey": "notif-20260401-001",
  "title": "Manutencao programada",
  "message": "Atualizacao de componentes iniciara em 5 minutos.",
  "mode": "notify_only",
  "severity": "medium",
  "eventType": "maintenance",
  "expiresAt": "2026-04-02T00:00:00Z",
  "metadata": {
    "ticket": "CHG-441"
  }
}
```

Resposta:

```json
{
  "accepted": true,
  "notificationId": "notif-20260401-001",
  "agentAction": "rendered"
}
```

## 5. Modelo de Comportamento de Notificacao

### 5.1 Modos

- `silent`: nao exibe UI ao usuario, apenas log/telemetria
- `notify_only`: exibe aviso e segue automaticamente
- `require_confirmation`: exige aprovacao do usuario antes de prosseguir

### 5.2 Resultado de confirmacao

Quando houver confirmacao obrigatoria, o agent deve produzir resultado explicito:

- `approved`
- `denied`
- `timeout_policy_applied`

## 6. Integracao PSADT no Agent

### 6.1 Evolucoes de backend

1. Estender `AgentConfiguration` e parsing para bloco `psadt` e blocos de notificacao.
2. No startup/sync, executar bootstrap assincromo do modulo quando:
   - `psadt.enabled = true`
   - `psadt.autoInstallModule = true`
3. Adicionar novo `installationType`: `PSAppDeployToolkit`.
4. No executor de automacao, criar branch PSADT:
   - validar modulo presente
   - `Import-Module PSAppDeployToolkit`
   - executar acao com timeout da policy
5. Em caso de falha, aplicar fallback conforme `fallbackPolicy`.

### 6.2 Ordem de prioridade de instalacao

Recomendado para install de pacote:

1. PSADT (se habilitado e valido)
2. Winget
3. Chocolatey (se policy permitir)

## 7. Notification Center

### 7.1 Backend

Criar um normalizador de eventos com envelope unico:

- origem: `api`, `automation`, `system`, `watchdog`
- tipo de evento
- criticidade
- modo de exibicao
- payload visual

Emitir para frontend via eventos Wails (`notification:new`).

### 7.2 Frontend

Implementar renderer com tres formatos:

- `toast` (informativo e temporario)
- `banner` (persistente)
- `modal` (confirmacao obrigatoria)

Aplicar:

- branding global (icone/banner/empresa)
- override por tipo de evento

## 8. Persistencia e Observabilidade

Registrar no agent:

- status do bootstrap PSADT (instalado, versao, origem, erro)
- notificacoes disparadas e resultado
- decisoes de usuario em confirmacoes

Eventos recomendados de log/telemetria:

- `install_module_attempt`
- `install_module_result`
- `notification_dispatched`
- `user_confirmation_result`

## 9. Plano de Implementacao por Fases

### Fase 1 - Contrato e governanca

1. Fechar schema dos blocos de configuracao.
2. Fechar schema de dispatch de notificacao.
3. Fechar matriz `mode x severity x action`.

### Fase 2 - Backend PSADT

1. Tipagem/parsing de config.
2. Bootstrap do modulo.
3. Novo installation type + executor.
4. Fallback policy.

### Fase 3 - UX de notificacao

1. Notification Center backend.
2. Eventos para frontend.
3. Renderer toast/banner/modal.
4. Confirmacao obrigatoria com retorno.

### Fase 4 - Rollout seguro

1. Rollout gradual por flags.
2. Testes de regressao Winget/Chocolatey/P2P.
3. Observabilidade e ajuste fino.

### Fase 5 - Documentacao de integracao e operacao

1. Criar documentacao tecnica da integracao com API contendo contratos de `configuration` e `notifications/dispatch` (campos obrigatorios, opcionais, defaults e exemplos completos).
2. Documentar fluxo de ponta a ponta do modulo de comportamento (`silent`, `notify_only`, `require_confirmation`) com diagramas de sequencia e resultados esperados (`approved`, `denied`, `timeout_policy_applied`).
3. Incluir tabela de mapeamento de `exit codes` PSADT (incluindo faixas reservadas) e como o agent decide sucesso, reboot, fallback e falha.
4. Documentar regras de fallback de instalacao (PSADT -> Winget -> Chocolatey), limites de timeout e comportamento em ambiente sem UI ativa (servico/headless).
5. Publicar guia de troubleshooting com logs/chaves de telemetria, erros comuns e checklist de validacao para equipe API, Agent e Frontend.

Artefato entregue: `DOCs/GUIA_INTEGRACAO_PSADT_API_OPERACAO.md`.

## 10. Validacao

1. Testes unitarios de parse/config com campos opcionais e retrocompatibilidade.
2. Testes de automacao para `PSAppDeployToolkit` (modulo presente/ausente, timeout, fallback).
3. Teste manual Windows em maquina limpa para auto-instalacao do modulo.
4. Teste de UX para `silent`, `notify_only`, `require_confirmation`.
5. Teste de troca de tema/branding sem rebuild.
6. Revisao da documentacao final com equipes Agent/API/Frontend para validar entendimento dos contratos e do modulo de comportamento.

## 11. Riscos e Mitigacoes

1. Restricoes de PowerShell/ExecutionPolicy: executar com politica adequada e logs claros.
2. Falha de rede na instalacao do modulo: retry + fallback de policy.
3. Ambientes sem UI ativa: obedecer policy e registrar resultado no backend.
4. Configuracoes visuais invalidas: validar payload (cores/URLs) antes de aplicar.

---

Responsavel tecnico sugerido: equipe Agent + equipe API (contrato) + equipe Frontend (renderer de notificacoes).
