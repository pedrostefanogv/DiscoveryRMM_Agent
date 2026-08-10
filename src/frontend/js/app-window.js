"use strict";

(function initWindowChrome() {
  var maxBtn = document.getElementById('windowMaxBtn');
  var closeBtn = document.getElementById('windowCloseBtn');
  var metaPCName = document.getElementById('windowMetaPCName');
  var metaDot = document.getElementById('windowMetaDot');

  var wailsMod = window.wails || {};

  // Métodos de janela: tenta o módulo Window nativo (v3) primeiro, depois os
  // helpers de conveniência do bridge. Em modo navegador (debug HTTP) ambos
  // são no-ops — comportamento esperado.
  function windowMethod(name, helperName) {
    if (wailsMod.Window && typeof wailsMod.Window[name] === 'function') {
      return wailsMod.Window[name];
    }
    if (typeof wailsMod[helperName] === 'function') {
      return wailsMod[helperName];
    }
    return null;
  }

  function runtimeReady() {
    return !!(windowMethod('ToggleMaximise', 'toggleMaximise') && windowMethod('Hide', 'hideWindow'));
  }

  function callWindow(fn, label) {
    if (!fn) {
      console.warn('[app-window] ' + label + ': metodo de janela indisponivel.', { wails: typeof window.wails });
      return false;
    }
    try {
      var ret = fn();
      if (ret && typeof ret.then === 'function') {
        ret.then(function () {
          console.log('[app-window] ' + label + ' ok');
        }, function (err) {
          console.error('[app-window] ' + label + ' falhou:', err && (err.message || err));
        });
      }
      return true;
    } catch (err) {
      console.error('[app-window] ' + label + ' erro ao chamar:', err);
      return false;
    }
  }

  function toggleMaximise() {
    var fn = windowMethod('ToggleMaximise', 'toggleMaximise');
    var dispatched = callWindow(fn, 'toggleMaximise');
    flashDiagnostic('max', dispatched);
  }

  function hideToTray() {
    var fn = windowMethod('Hide', 'hideWindow');
    var dispatched = callWindow(fn, 'hideWindow');
    flashDiagnostic('close', dispatched);
  }

  function flashDiagnostic(which, dispatched) {
    try {
      var el = document.getElementById(which === 'close' ? 'windowCloseBtn' : 'windowMaxBtn');
      if (!el) return;
      var base = el.title.split('|')[0].trim();
      el.title = (base || 'agente') + ' | ' + (dispatched ? 'enviado' : 'sem-metodo');
      var prev = el.style.backgroundColor;
      el.style.backgroundColor = dispatched ? 'rgba(76,175,80,.5)' : 'rgba(193,18,31,.5)';
      setTimeout(function () { el.style.backgroundColor = prev; }, 400);
    } catch (e) { /* não crítico */ }
  }

  function updateWindowMeta() {
    if (document.hidden) return;
    if (!(window.go && window.go.app && window.go.app.App && typeof window.go.app.App.GetStatusOverview === 'function')) {
      return;
    }

    window.go.app.App.GetStatusOverview().then(function (status) {
      var hostname = (status && status.hostname) ? status.hostname : '-';
      if (metaPCName) metaPCName.textContent = translate('window.meta.pc') + ': ' + hostname;
      if (metaDot) {
        var online = !!(status && status.connected);
        metaDot.classList.toggle('online', online);
        metaDot.classList.toggle('offline', !online);
      }
      // Mantém a versão do sidebar sempre em dia no boot/visível, sem
      // depender de abrir a página Status.
      var appVersion = (status && status.appVersion) ? String(status.appVersion).trim() : '';
      if (appVersion) {
        var sidebarVersion = document.querySelector('.sidebar-version');
        if (sidebarVersion && sidebarVersion.textContent !== 'v' + appVersion) {
          sidebarVersion.textContent = 'v' + appVersion;
        }
      }
    }).catch(function () {
      if (metaPCName) metaPCName.textContent = translate('window.meta.pc') + ': -';
      if (metaDot) {
        metaDot.classList.add('offline');
        metaDot.classList.remove('online');
      }
    });
  }

  if (maxBtn) {
    maxBtn.addEventListener('click', function (e) {
      e.preventDefault();
      toggleMaximise();
    });
    maxBtn.addEventListener('dblclick', function (e) {
      e.preventDefault();
      toggleMaximise();
    });
  }

  if (closeBtn) {
    closeBtn.addEventListener('click', function (e) {
      e.preventDefault();
      hideToTray();
    });
  }

  console.log('[app-window] init: max=', !!maxBtn, 'close=', !!closeBtn,
    'runtimeReady=', runtimeReady(),
    'toggleMaximise=', window.wails && typeof window.wails.toggleMaximise,
    'hideWindow=', window.wails && typeof window.wails.hideWindow);

  var windowMetaPollId = null;
  var WINDOW_META_POLL_MS = 4000; // alinhado à página de Status (evita defasagem)

  function startWindowMetaPoll() {
    stopWindowMetaPoll();
    updateWindowMeta();
    windowMetaPollId = setInterval(updateWindowMeta, WINDOW_META_POLL_MS);
  }

  function stopWindowMetaPoll() {
    if (windowMetaPollId) {
      clearInterval(windowMetaPollId);
      windowMetaPollId = null;
    }
  }

  document.addEventListener('visibilitychange', function () {
    if (document.hidden) {
      stopWindowMetaPoll();
      return;
    }
    startWindowMetaPoll();
  });

  if (!document.hidden) {
    startWindowMetaPoll();
  }

  // Atualização imediata via evento dedicado do backend (agent:connectivity).
  // Quando o estado online/offline muda, o Go emite este evento e atualizamos
  // a bolinha, o tooltip e (se visível) o status sem esperar o polling.
  var lastConnectivityState = null;

  function applyConnectivityEvent(data) {
    // O evento do backend indica que a conectividade mudou, mas a FONTE de
    // verdade do indicador é o estado consolidado de GetStatusOverview()
    // (que considera o transporte E o pong global do sync). Usamos o evento
    // apenas como gatilho para uma atualização imediata, evitando conflito
    // entre duas fontes divergentes (transporte vs. sinal online).
    if (!(window.go && window.go.app && window.go.app.App && typeof window.go.app.App.GetStatusOverview === 'function')) {
      return;
    }
    window.go.app.App.GetStatusOverview().then(function (status) {
      if (!status) return;
      var connected = !!status.connected;
      lastConnectivityState = { connected: connected, transport: (status && status.connectionType) || '' };

      if (metaDot) {
        metaDot.classList.toggle('online', connected);
        metaDot.classList.toggle('offline', !connected);
      }
      if (metaPCName && status.hostname) {
        metaPCName.textContent = translate('window.meta.pc') + ': ' + status.hostname;
      }
      if (typeof window.__connectivityEventPing === 'function') {
        try {
          window.__connectivityEventPing(connected, lastConnectivityState.transport, 'event');
        } catch (e) { /* não crítico */ }
      }
    }).catch(function () {
      if (typeof window.__connectivityEventPing === 'function') {
        try {
          window.__connectivityEventPing(false, '', 'event');
        } catch (e) { /* não crítico */ }
      }
    });
  }

  // Expõe o último estado para o app-status.js re-aplicar ao ser carregado.
  window.__lastConnectivityState = function () {
    return lastConnectivityState;
  };

  if (wailsMod.on && typeof wailsMod.on === 'function') {
    wailsMod.on('agent:connectivity', applyConnectivityEvent);
  }
})();
