# Guia de Integracao PSADT + Notificacoes (API, Agent, Frontend)

## 1. Objetivo

Este documento consolida os contratos e fluxos operacionais da integracao entre API, Agent (backend Go/Wails) e Frontend para:

- Bootstrap e execucao de instalacoes via PSAppDeployToolkit (PSADT).
- Dispatch de notificacoes para usuario final em modos `silent`, `notify_only` e `require_confirmation`.
- Decisao de fallback para gestores de pacote (`winget` e `chocolatey`).

## 2. Contrato de Configuracao (`/configuration`)

### 2.1 Bloco `psadt`

Campos:

- `enabled` (bool, opcional): habilita o caminho PSADT.
- `requiredVersion` (string, opcional, default `4.1.8`): versao minima esperada.
- `autoInstallModule` (bool, opcional, default `true`): tenta instalar modulo automaticamente.
- `installSource` (string, opcional, default `powershell_gallery`): origem de instalacao.
- `executionTimeoutSeconds` (int, opcional, default `1800`).
- `fallbackPolicy` (string, opcional, default `winget_then_choco`).
- `installOnStartup` (bool, opcional, default `true`).
- `installOnDemand` (bool, opcional, default `true`).
- `successExitCodes` ([]int, opcional, default `[0,3010]`).
- `rebootExitCodes` ([]int, opcional, default `[1641,3010]`).
- `ignoreExitCodes` ([]int, opcional).
- `timeoutAction` (string, opcional, default `fail`).
- `unknownExitCodePolicy` (string, opcional, default `recoverable_failure`).

### 2.2 Bloco `notificationBranding`

Campos:

- `companyName`, `logoUrl`, `bannerUrl`.
- `theme`: `surface`, `text`, `accent`, `success`, `warning`, `danger`.

### 2.3 Bloco `notificationPolicies`

Lista de politicas por evento. Campos:

- `eventType`, `mode`, `severity`, `timeoutSeconds`.
- `styleOverride`: `layout`, `background`, `text`.
- `actions`: lista de acoes (`id`, `label`, `actionType`).

### 2.4 Bloco `rollout` (kill switches e gates)

Campos:

- `enableNotifications` (bool, opcional, default `true`): kill switch global de notificacoes.
- `enableRequireConfirmation` (bool, opcional, default `true`): se `false`, faz downgrade de `require_confirmation` para `notify_only`.
- `enablePsadtBootstrap` (bool, opcional, default `true`): kill switch do bootstrap automatico do modulo PSADT.
- `allowedNotificationEventTypes` ([]string, opcional): allow list de `eventType`.
- `blockedNotificationEventTypes` ([]string, opcional): deny list de `eventType` (tem precedencia sobre allow list).

## 3. Contrato de Dispatch de Notificacao (`/notifications/dispatch`)

Payload esperado:

- `notificationId` (string, opcional).
- `idempotencyKey` (string, opcional): evita duplicidade.
- `title` (string, opcional; default `Notificacao`).
- `message` (string, opcional).
- `mode` (string, opcional; default `notify_only`): `silent`, `notify_only`, `require_confirmation`.
- `severity` (string, opcional; default `medium`): normalizada para `low|medium|high|critical`.
- `eventType` (string, opcional).
- `layout` (string, opcional; default `toast`): `toast|banner|modal`.
- `timeoutSeconds` (int, opcional; default `45`).
- `metadata` (objeto, opcional).

Resposta do Agent:

- `accepted` (bool).
- `notificationId` (string).
- `agentAction` (string): `rendered`, `user_decision`, `timeout`, `deduplicated`, `disabled_by_rollout`, etc.
- `result` (string): `approved`, `denied`, `timeout_policy_applied`.
- `message` (string, opcional).

## 4. Fluxo de Modos

### 4.1 `silent`

- Frontend nao exige interacao.
- Agent considera a notificacao processada sem bloqueio.

### 4.2 `notify_only`

- Notificacao exibida em UI.
- Agent retorna sem aguardar confirmacao do usuario.
- Resultado final: `approved`.

### 4.3 `require_confirmation`

- Agent aguarda resposta do frontend ate `timeoutSeconds`.
- Respostas validas: `approved` e `denied`.
- Sem resposta no prazo: `timeout_policy_applied`.
- Se `rollout.enableRequireConfirmation=false`, o modo e convertido para `notify_only`.

## 5. Mapeamento de Exit Codes PSADT

Categorias praticas usadas pelo agent:

- `success`: codigos de sucesso (default inclui `0` e `3010`).
- `reboot_required`: codigos que indicam reboot (default inclui `1641` e `3010`).
- `recoverable_failure`: falha com possibilidade de fallback.
- `fatal_failure`: falha sem fallback.

Recomendacoes operacionais:

- Tratar `3010` como sucesso com reboot pendente.
- Reservar faixa interna de codigos de automacao para padroes de erro (ex.: parsing/comando/timeout).
- Manter alinhamento entre lista da API e classificacao local para evitar divergencia de decisao.

## 6. Regras de Fallback

Ordem padrao:

1. PSADT
2. `winget`
3. `chocolatey`

Quando aplicar fallback:

- Falhas classificadas como recuperaveis no PSADT.
- Politica de fallback ativa (`fallbackPolicy`).

Quando nao aplicar fallback:

- Falhas fatais.
- Politica explicitamente desabilitada.

## 7. Comportamento em Ambiente sem UI (headless/servico)

- Em `notify_only`: resultado `approved` com acao de log/registro.
- Em `require_confirmation`: aplica `timeout_policy_applied` se nao houver contexto de UI.
- Todos os eventos relevantes devem ser persistidos para auditoria.

## 8. Observabilidade e Troubleshooting

Eventos recomendados:

- `install_module_attempt`
- `install_module_result`
- `notification_dispatched`
- `user_confirmation_result`

Checklist rapido:

1. Confirmar recebimento de `configuration` pelo agent.
2. Validar `rollout.enableNotifications` e allow/block list.
3. Verificar se o modulo PSADT esta instalado na versao esperada.
4. Confirmar caminho de fallback quando PSADT falhar.
5. Correlacionar registros de dispatch e resposta de notificacao.

Erros comuns:

- Timeout de confirmacao por UI indisponivel.
- Evento bloqueado por deny list de rollout.
- Divergencia entre `successExitCodes` da API e classificacao local.
- Falta de permissao para instalacao do modulo PSADT.

## 9. Exemplo Completo de Configuracao

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
    "installOnDemand": true,
    "successExitCodes": [0, 3010],
    "rebootExitCodes": [1641, 3010],
    "ignoreExitCodes": [42],
    "timeoutAction": "fail",
    "unknownExitCodePolicy": "recoverable_failure"
  },
  "notificationBranding": {
    "companyName": "Meduza",
    "logoUrl": "https://example/logo.svg",
    "bannerUrl": "https://example/banner.png",
    "theme": {
      "surface": "#111827",
      "text": "#f9fafb",
      "accent": "#0ea5e9",
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
      },
      "actions": [
        {
          "id": "details",
          "label": "Ver detalhes",
          "actionType": "open_logs"
        }
      ]
    }
  ],
  "rollout": {
    "enableNotifications": true,
    "enableRequireConfirmation": true,
    "enablePsadtBootstrap": true,
    "allowedNotificationEventTypes": ["install_start", "install_end"],
    "blockedNotificationEventTypes": []
  }
}
```
