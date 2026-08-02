"use strict";

(function () {

  function ztcEl(id) {
    return document.getElementById(id);
  }

  function p2pApi() {
    return window.go.app.App;
  }

  function renderZtcStats(stats) {
    var statusEl = ztcEl('ztcStatus');
    var eventsEl = ztcEl('ztcEvents');
    if (!statusEl && !eventsEl) return;

    var s = stats || { enabled: false, totalProvisioned: 0, recentEvents: [] };

    if (statusEl) {
      var rows = [
        ['Ativo', s.enabled ? 'sim' : 'nao'],
        ['Agentes provisionados', String(s.totalProvisioned || 0)]
      ];
      statusEl.innerHTML = rows.map(function (r) {
        return '<div class="fact"><div class="k">' + escapeHtml(r[0]) + '</div><div class="v mono">' + escapeHtml(r[1]) + '</div></div>';
      }).join('');
    }

    if (eventsEl) {
      var events = s.recentEvents || [];
      if (!events.length) {
        eventsEl.innerHTML = '<div class="automation-task-card"><div class="meta">' + escapeHtml(translate('ztc.noEvents')) + '</div></div>';
      } else {
        eventsEl.innerHTML = events.map(function (ev) {
          var badge = ev.success ? 'success' : 'error';
          return '<article class="automation-task-card">' +
            '<div class="automation-task-top">' +
            '<h4 class="mono">' + escapeHtml(ev.sourceAgentId || '-') + '</h4>' +
            '<span class="automation-execution-badge ' + badge + '">' + (ev.success ? 'ok' : 'erro') + '</span>' +
            '</div>' +
            '<div class="automation-task-meta">' + escapeHtml(formatDate(ev.timestampUtc)) + '</div>' +
            (ev.serverUrl ? '<div class="automation-task-desc">Servidor: ' + escapeHtml(ev.serverUrl) + '</div>' : '') +
            '<div class="meta">' + escapeHtml(ev.message || '-') + '</div>' +
            '</article>';
        }).join('');
      }
    }
  }

  async function loadZtcView() {
    var view = ztcEl('zeroTouchConfigView');
    if (!view || view.classList.contains('hidden')) return;

    var statusEl = ztcEl('ztcStatus');
    if (statusEl) {
      statusEl.innerHTML = '<div class="meta">' + escapeHtml(translate('ztc.loading')) + '</div>';
    }

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
