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
        // "Online" reflete o transporte conectado (fonte confiável do agentconn),
        // com fallback para o campo connected consolidado caso transportConnected
        // não venha preenchido. Evita correlacionar o indicador com o status do
        // pong global (que pode ficar stale sem derrubar o transporte).
        var transportUp = !!(status && (status.transportConnected || status.connected));
        metaDot.classList.toggle('online', transportUp);
        metaDot.classList.toggle('offline', !transportUp);
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

  // ── Fallback de acessibilidade (DPI scaling / janela cortada) ──
  // Em telas 1080p com escala 125%/150% a janela pode abrir com o chrome
  // (min/max/close) fora da área visível. O backend já aplica clamp via
  // FitWindowToWorkArea, mas estes atalhos servem como rede de segurança:
  //   Ctrl+Alt+M  → maximizar/restaurar
  //   Ctrl+Alt+W  → fechar para tray
  //   Ctrl+Alt+D  → diagnóstico no console (tamanho da janela vs viewport)
  function registerKeyboardFallback() {
    document.addEventListener('keydown', function (e) {
      if (!(e.ctrlKey && e.altKey)) return;
      var key = (e.key || '').toLowerCase();
      if (key === 'm') {
        e.preventDefault();
        toggleMaximise();
      } else if (key === 'w') {
        e.preventDefault();
        hideToTray();
      } else if (key === 'd') {
        e.preventDefault();
        console.log('[app-window] diagnostico janela:', {
          innerWidth: window.innerWidth,
          innerHeight: window.innerHeight,
          outerWidth: window.outerWidth,
          outerHeight: window.outerHeight,
          devicePixelRatio: window.devicePixelRatio,
          chromeVisible: isChromeVisible()
        });
      }
    });
  }

  // Detecta se o chrome da janela está fora da área visível (ex.: janela
  // posicionada acima do topo do monitor). Se detectado, maximiza como
  // recuperação automática — o usuário recupera o controle dos botões.
  function isChromeVisible() {
    var chrome = document.getElementById('windowChrome');
    if (!chrome) return true;
    var rect = chrome.getBoundingClientRect();
    return rect.bottom > 0 && rect.top < window.innerHeight && rect.width > 0;
  }

  function ensureChromeAccessible(attempt) {
    attempt = attempt || 0;
    if (isChromeVisible()) return;
    // Modo navegador (debug HTTP): sem métodos de janela nativos — nada a
    // fazer além de logar. Evita tentativas inúteis em loop.
    var maximiseFn = windowMethod('Maximise', 'maximiseWindow');
    if (!maximiseFn) {
      if (attempt === 0) {
        console.warn('[app-window] chrome fora da area visivel, mas sem metodo de janela (modo navegador?)');
      }
      return;
    }
    console.warn('[app-window] chrome fora da area visivel; maximizando como recuperacao (tentativa ' + (attempt + 1) + ')');
    callWindow(maximiseFn, 'maximise');
    // Re-verifica: se o chrome ainda estiver invisível (ex.: maximise falhou
    // ou o clamp do backend reposicionou depois), tenta mais 2 vezes com
    // backoff. O clamp do backend roda no WindowShow e pode chegar depois
    // desta verificação — o retry evita maximizar indevidamente nesse caso.
    if (attempt < 2) {
      setTimeout(function () { ensureChromeAccessible(attempt + 1); }, 1000 * (attempt + 1));
    }
  }

  registerKeyboardFallback();
  // Verifica após o primeiro layout (o clamp do backend roda no WindowShow;
  // esta verificação cobre o caso residual com retries espaçados).
  setTimeout(function () { ensureChromeAccessible(0); }, 1500);

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
  // Quando o estado online/offline muda, o Go emite este evento com o estado
  // REAL de conectividade (transport conectado / perda de conexão). Aplicamos
  // esse estado diretamente para refletir na hora o que o backend já sabe.
  var lastConnectivityState = null;

  function applyConnectivityEvent(data) {
    if (!data) return;
    var connected = !!data.connected;
    var transport = (data && data.transport) || '';
    lastConnectivityState = { connected: connected, transport: transport };

    if (metaDot) {
      metaDot.classList.toggle('online', connected);
      metaDot.classList.toggle('offline', !connected);
    }

    if (typeof window.__connectivityEventPing === 'function') {
      try {
        window.__connectivityEventPing(connected, transport, 'event');
      } catch (e) { /* não crítico */ }
    }
  }

  // Expõe o último estado para o app-status.js re-aplicar ao ser carregado.
  window.__lastConnectivityState = function () {
    return lastConnectivityState;
  };

  if (wailsMod.on && typeof wailsMod.on === 'function') {
    wailsMod.on('agent:connectivity', applyConnectivityEvent);
  }
})();
