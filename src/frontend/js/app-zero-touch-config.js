"use strict";

(function () {

  function ztcEl(id) {
    return document.getElementById(id);
  }

  function p2pApi() {
    return window.go.app.App;
  }

  function ztcEscapeHtml(text) {
    return String(text || '')
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;');
  }

  function ztcFormatDate(raw) {
    if (!raw) return '-';
    try {
      var d = new Date(raw);
      if (isNaN(d.getTime())) return String(raw);
      return d.toLocaleDateString('pt-BR') + ', ' + d.toLocaleTimeString('pt-BR');
    } catch (_) {
      return String(raw);
    }
  }

  function renderZtcStats(stats) {
    var statusEl = ztcEl('ztcStatus');
    var eventsEl = ztcEl('ztcEvents');
    if (!statusEl && !eventsEl) return;

    var s = stats || { enabled: false, totalProvisioned: 0, recentEvents: [] };

    if (statusEl) {
      var rows = [
        ['Ativo', s.enabled ? 'sim' : 'nao'],
        ['Agentes provisionados', String(s.totalProvisioned || 0)],
        ['Endpoint', '/p2p/config/onboard (GET)']
      ];
      statusEl.innerHTML = rows.map(function (r) {
        return '<div class="fact"><div class="k">' + ztcEscapeHtml(r[0]) + '</div><div class="v mono">' + ztcEscapeHtml(r[1]) + '</div></div>';
      }).join('');
    }

    if (eventsEl) {
      var events = s.recentEvents || [];
      if (!events.length) {
        eventsEl.innerHTML = '<div class="automation-task-card"><div class="meta">Nenhum evento de provisionamento Zero-Touch registrado.</div></div>';
      } else {
        eventsEl.innerHTML = events.map(function (ev) {
          var badge = ev.success ? 'success' : 'error';
          return '<article class="automation-task-card">' +
            '<div class="automation-task-top">' +
            '<h4 class="mono">' + ztcEscapeHtml(ev.sourceAgentId || '-') + '</h4>' +
            '<span class="automation-execution-badge ' + badge + '">' + (ev.success ? 'ok' : 'erro') + '</span>' +
            '</div>' +
            '<div class="automation-task-meta">' + ztcEscapeHtml(ztcFormatDate(ev.timestampUtc)) + '</div>' +
            (ev.serverUrl ? '<div class="automation-task-desc">Servidor: ' + ztcEscapeHtml(ev.serverUrl) + '</div>' : '') +
            '<div class="meta">' + ztcEscapeHtml(ev.message || '-') + '</div>' +
            '</article>';
        }).join('');
      }
    }
  }

  async function loadZtcView() {
    var view = ztcEl('zeroTouchConfigView');
    if (!view || view.classList.contains('hidden')) return;

    try {
      var stats = await p2pApi().GetAutoProvisioningStats();
      renderZtcStats(stats || {});
    } catch (_) {
      renderZtcStats({ enabled: false, totalProvisioned: 0, recentEvents: [] });
    }
  }

  window.refreshZeroTouchConfig = loadZtcView;

  var refreshBtn = ztcEl('ztcRefreshBtn');
  if (refreshBtn) {
    refreshBtn.addEventListener('click', loadZtcView);
  }

  setTimeout(function () {
    loadZtcView();
  }, 0);

})();
