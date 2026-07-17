"use strict";

// ─────────────────────────────────────────────────────────────────────────────
// Debug HTTP Bridge — Fallback HTTP quando rodando fora do WebView2 (navegador)
// ─────────────────────────────────────────────────────────────────────────────
// Este arquivo é carregado antes dos scripts da aplicação e detecta se o
// runtime Wails está disponível. Se não estiver (navegador comum), intercepta
// as chamadas a `window.go.main.App.*` e as redireciona para a API HTTP local.
// ─────────────────────────────────────────────────────────────────────────────

(function () {
  // O bridge HTTP deve rodar SOMENTE quando a UI estiver aberta em navegador
  // no loopback local (127.0.0.1/localhost). Em runtime nativo (Wails),
  // não devemos sobrescrever window.runtime/window.go.
  var protocol = String(location.protocol || '').toLowerCase();
  var host = String(location.hostname || '').toLowerCase();
  var isHTTP = protocol === 'http:' || protocol === 'https:';
  var isLoopback = host === '127.0.0.1' || host === 'localhost' || host === '::1' || host === '[::1]';

  if (!isHTTP || !isLoopback) {
    console.log('[debug-http] ambiente nativo detectado; mantendo runtime Wails');
    return;
  }

  // Se o runtime Wails já estiver disponível, não habilita fallback.
  if (typeof window.go !== 'undefined' && window.go && window.go.main && window.go.main.App) {
    console.log('[debug-http] runtime Wails detectado no navegador local — usando bridge nativo');
    return;
  }

  console.log('[debug-http] WebView2 nao detectado — ativando bridge HTTP');

  // No modo navegador, reutiliza a origem atual da página debug HTTP.
  var API_BASE = location.origin + '/api/';

  // Cria um proxy que intercepta chamadas a window.go.main.App.*
  function createGoBridge() {
    var handler = {
      get: function (target, methodName) {
        if (typeof methodName !== 'string') {
          return target[methodName];
        }

        // Retorna uma função que faz a chamada HTTP
        return function () {
          var args = Array.prototype.slice.call(arguments);
          console.log('[debug-http] bridge HTTP: ' + methodName);

          var fetchOpts = {
            method: 'GET',
            headers: { 'Content-Type': 'application/json' },
          };

          // Se tem argumentos, faz POST com body JSON
          if (args.length > 0 && args[0] !== undefined && args[0] !== null) {
            fetchOpts.method = 'POST';
            fetchOpts.body = JSON.stringify(args[0]);
          }

          return fetch(API_BASE + methodName, fetchOpts)
            .then(function (response) {
              if (!response.ok) {
                return response.json().then(function (err) {
                  throw new Error(err.error || 'HTTP ' + response.status);
                });
              }
              if (response.status === 204) {
                return null;
              }
              return response.json();
            })
            .catch(function (err) {
              console.error('[debug-http] erro em ' + methodName + ':', err);
              throw err;
            });
        };
      }
    };

    return new Proxy({}, handler);
  }

  // Injeta a estrutura window.go.main.App como proxy HTTP
  window.go = {
    main: {
      App: createGoBridge(),
    },
    app: {
      App: createGoBridge(),
    },
  };

  // ── SSE-based EventsOn/EventsOff para streaming (chat, etc.) ───
  // No navegador, conectamos ao endpoint SSE /api/chat-events para
  // receber eventos que o backend emite via Wails EventsEmit.
  var sseEventSource = null;
  var sseListeners = {}; // { eventName: [callback, ...] }
  var sseReconnectTimer = null;
  var sseReconnectDelay = 500;

  function ensureSSEConnection() {
    if (sseEventSource && sseEventSource.readyState !== EventSource.CLOSED) {
      return;
    }
    var url = location.origin + '/api/chat-events';
    console.log('[debug-http] conectando SSE: ' + url);
    sseEventSource = new EventSource(url);

    sseEventSource.onmessage = function (msg) {
      try {
        var parsed = JSON.parse(msg.data);
        var eventType = parsed.event;
        var eventData = parsed.data;
        var callbacks = sseListeners[eventType];
        if (callbacks && callbacks.length > 0) {
          for (var i = 0; i < callbacks.length; i++) {
            try {
              callbacks[i](eventData);
            } catch (e) {
              console.error('[debug-http] erro no listener SSE ' + eventType + ':', e);
            }
          }
        }
      } catch (e) {
        // ignora mensagens não-JSON
      }
    };

    sseEventSource.onerror = function () {
      console.warn('[debug-http] erro na conexao SSE, reconectando...');
      if (sseEventSource) {
        sseEventSource.close();
        sseEventSource = null;
      }
      // Reconexão com backoff
      if (sseReconnectTimer) clearTimeout(sseReconnectTimer);
      sseReconnectTimer = setTimeout(function () {
        sseReconnectDelay = Math.min(sseReconnectDelay * 2, 10000);
        ensureSSEConnection();
      }, sseReconnectDelay);
    };

    sseEventSource.onopen = function () {
      console.log('[debug-http] SSE conectado');
      sseReconnectDelay = 500; // reset backoff
    };
  }

  function EventsOn(eventName, callback) {
    if (!sseListeners[eventName]) {
      sseListeners[eventName] = [];
    }
    sseListeners[eventName].push(callback);
    // Inicia conexão SSE lazy — só quando alguém se inscreve
    ensureSSEConnection();
  }

  function EventsOff(eventName) {
    delete sseListeners[eventName];
  }

  function EventsOffAll() {
    sseListeners = {};
    if (sseEventSource) {
      sseEventSource.close();
      sseEventSource = null;
    }
    if (sseReconnectTimer) {
      clearTimeout(sseReconnectTimer);
      sseReconnectTimer = null;
    }
  }

  window.runtime = {
    LogPrint: function (msg) { console.log('[wails]', msg); },
    LogTrace: function (msg) { console.debug('[wails]', msg); },
    LogDebug: function (msg) { console.debug('[wails]', msg); },
    LogInfo: function (msg) { console.info('[wails]', msg); },
    LogWarning: function (msg) { console.warn('[wails]', msg); },
    LogError: function (msg) { console.error('[wails]', msg); },
    LogFatal: function (msg) { console.error('[wails][FATAL]', msg); },
    WindowToggleMaximise: function () { console.log('[wails] WindowToggleMaximise (no-op no browser)'); },
    WindowMaximise: function () {},
    WindowUnmaximise: function () {},
    WindowMinimise: function () {},
    WindowShow: function () {},
    WindowHide: function () { console.log('[wails] WindowHide (no-op no browser)'); },
    WindowSetTitle: function () {},
    WindowReload: function () { location.reload(); },
    WindowReloadApp: function () { location.reload(); },
    WindowSetAlwaysOnTop: function () {},
    WindowSetSystemDefaultTheme: function () {},
    WindowSetLightTheme: function () {},
    WindowSetDarkTheme: function () {},
    WindowCenter: function () {},
    WindowFullscreen: function () {},
    WindowUnfullscreen: function () {},
    WindowIsFullscreen: function () { return false; },
    WindowGetSize: function () { return { w: window.innerWidth, h: window.innerHeight }; },
    WindowSetSize: function () {},
    WindowSetMaxSize: function () {},
    WindowSetMinSize: function () {},
    WindowSetPosition: function () {},
    WindowGetPosition: function () { return { x: 0, y: 0 }; },
    EventsOn: EventsOn,
    EventsOff: EventsOff,
    EventsOffAll: EventsOffAll,
    EventsOnMultiple: EventsOn,
    EventsEmit: function () {}
  };

  console.log('[debug-http] bridge HTTP ativo — ' + API_BASE);
})();
