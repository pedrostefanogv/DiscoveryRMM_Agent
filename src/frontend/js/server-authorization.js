"use strict";

// server-authorization.js — estado ÚNICO de "provisionado, aguardando liberação
// do servidor" no frontend.
//
// ── Problema (relato 2026-11-17) ────────────────────────────────────────────
// O agente recebeu o token via zero-touch e ficou aguardando autorização no
// servidor. Nessa janela a overlay de onboarding era a única superfície que
// sabia do estado (e só quando o flag zeroTouchPending já tinha chegado ao
// processo que roda o sync). Loja, Suporte e Base de Conhecimento caíam no
// caminho de erro e mostravam o 403 CRU do backend:
//   • "RuntimeError: app-store (Winget) retornou HTTP 403 Forbidden:
//      {\"error\":\"Agent registration is pending (zero-touch).\"}"
//   • "Erro ao carregar chamados: RuntimeError: ipc request support:tickets
//      falhou: HTTP 403 Forbidden: {...}"
//   • "Erro ao carregar base de conhecimento."
//
// ── Regra ───────────────────────────────────────────────────────────────────
// O modo de onboarding vem do GO (GetOnboardingStatus), que é a fonte da
// verdade (lê o /me/configuration e a pendência zero-touch). Este módulo só
// CACHEIA esse modo, avisa quem depende dele (telas de conteúdo) e substitui a
// mensagem de erro por uma TELA DE ESPERA quando o modo é "awaiting-approval".
//
// Estes scripts clássicos compartilham o mesmo escopo global (ver
// bootstrap-partials.js): as funções abaixo ficam disponíveis para
// app-store-updates.js, app-support.js e app-knowledge.js, que carregam depois.

var __onboardingMode = 'normal';
var __onboardingPendingReason = '';
var __onboardingAcceptedAt = '';
var __serverAuthListenersBound = false;
var __serverAuthWaiters = [];

// Mensagem única de espera. Mantida em sincronia com provisioning.approval.*
// (app-utils.js) e com onboardingApprovalMessage do Go
// (server_authorization.go) — três lugares, um texto.
function serverAuthWaitingTitle() {
  return translate('provisioning.approval.title');
}

function serverAuthWaitingMessage() {
  return translate('serverAuth.pending.message');
}

// serverAuthorizationMode devolve o modo atual ('normal' | 'awaiting-approval' |
// 'awaiting-auto-provisioning').
function serverAuthorizationMode() {
  return __onboardingMode;
}

// isServerAuthorizationPending: agente provisionado, mas o servidor ainda não
// liberou o acesso.
function isServerAuthorizationPending() {
  return __onboardingMode === 'awaiting-approval';
}

// isOnboardingBlocked: qualquer estado em que o agente não pode operar contra o
// servidor (sem credenciais OU aguardando liberação).
function isOnboardingBlocked() {
  return __onboardingMode === 'awaiting-approval' ||
    __onboardingMode === 'awaiting-auto-provisioning';
}

// applyServerAuthorizationState é chamada pelo overlay (que já consulta o
// GetOnboardingStatus a cada 10s e em eventos) e por consultas diretas.
function applyServerAuthorizationState(onboardingStatus) {
  var mode = String(onboardingStatus && onboardingStatus.mode || '').trim().toLowerCase();
  if (mode !== 'awaiting-approval' && mode !== 'awaiting-auto-provisioning' && mode !== 'normal') {
    return;
  }
  var reason = String(onboardingStatus && onboardingStatus.pendingReason || '').trim();
  var acceptedAt = String(onboardingStatus && onboardingStatus.acceptedAtUtc || '').trim();
  var changed = mode !== __onboardingMode ||
    reason !== __onboardingPendingReason ||
    acceptedAt !== __onboardingAcceptedAt;
  __onboardingMode = mode;
  __onboardingPendingReason = reason;
  __onboardingAcceptedAt = acceptedAt;
  if (changed) {
    var waiters = __serverAuthWaiters.slice();
    __serverAuthWaiters = [];
    waiters.forEach(function (fn) {
      try { fn(mode); } catch (_) { /* listener de tela: não pode derrubar o estado */ }
    });
  }
}

// onServerAuthorizationChange registra um listener chamado quando o modo MUDA.
// O listener é chamado IMEDIATAMENTE se o modo atual já for de espera (evita o
// caso "a tela carregou depois do evento").
function onServerAuthorizationChange(fn) {
  if (typeof fn !== 'function') return;
  __serverAuthWaiters.push(fn);
  if (__onboardingMode !== 'normal') {
    try { fn(__onboardingMode); } catch (_) { /* idem */ }
  }
}

// refreshServerAuthorizationState consulta o GO sob demanda (entrada de uma aba
// que ainda não recebeu o estado) e notifica os listeners.
function refreshServerAuthorizationState() {
  try {
    return appApi().GetOnboardingStatus().then(function (st) {
      applyServerAuthorizationState(st);
      return st;
    }).catch(function () { return null; });
  } catch (_) {
    return Promise.resolve(null);
  }
}

// notifyServerAuthorizationChanged reavalia o estado em EVENTOS do backend
// (agent:onboarding) e ao reconectar o IPC — sem esperar o próximo tick.
function bindServerAuthorizationListeners() {
  if (__serverAuthListenersBound) return;
  __serverAuthListenersBound = true;
  var on = null;
  if (window.wails && typeof window.wails.on === 'function') {
    on = window.wails.on.bind(window.wails);
  } else if (window.runtime && typeof window.runtime.EventsOn === 'function') {
    on = window.runtime.EventsOn.bind(window.runtime);
  }
  if (!on) return;
  try {
    on('agent:onboarding', function () { refreshServerAuthorizationState(); });
    on('service:ipc_state', function (data) {
      if (data && data.connected) refreshServerAuthorizationState();
    });
  } catch (_) { /* listeners opcionais */ }
}

// renderServerAuthWaitingState desenha a TELA DE ESPERA no lugar do erro. Usada
// por Loja/Suporte/Base de Conhecimento — o usuário passa a entender que falta
// liberação no servidor, em vez de ver um erro técnico de API.
function renderServerAuthWaitingState(container, opts) {
  if (!container) return;
  var options = opts || {};
  var detail = String(options.detail || serverAuthWaitingMessage());
  var html = '<div class="kb-empty-state server-auth-pending">' +
    '<div class="kb-empty-icon">&#9203;</div>' +
    '<div class="kb-empty-text">' +
    escapeHtml(serverAuthWaitingTitle()) +
    '</div>' +
    '<div class="meta">' + escapeHtml(detail) + '</div>' +
    '<button type="button" class="btn subtle kb-retry-btn" id="' +
    escapeHtml(options.retryId || 'serverAuthRetryBtn') + '">' +
    escapeHtml(translate('provisioning.refresh')) +
    '</button>' +
    '</div>';
  container.innerHTML = html;
  var retry = document.getElementById(options.retryId || 'serverAuthRetryBtn');
  if (retry && typeof options.onRetry === 'function') {
    retry.addEventListener('click', options.onRetry);
  }
}

// serverAuthLooksLikePendingError reconhece o 403 de agente pendente em uma
// mensagem de erro qualquer. É a rede de segurança de quando o estado do GO
// ainda não chegou mas a própria chamada já provou a pendência.
function serverAuthLooksLikePendingError(err) {
  var text = String(err && (err.message || err) || '').toLowerCase();
  if (!text) return false;
  return text.indexOf('registration is pending') >= 0 ||
    text.indexOf('aguardando aprovacao') >= 0 ||
    text.indexOf('aguardando aprovação') >= 0;
}

bindServerAuthorizationListeners();
