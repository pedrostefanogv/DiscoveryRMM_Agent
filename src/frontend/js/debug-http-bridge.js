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

  // Também mockamos o window.runtime para evitar erros em chamadas que usam
  // funções do Wails runtime (WindowToggleMaximise, WindowHide, etc.)
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
    EventsOn: function () {},
    EventsOff: function () {},
    EventsOffAll: function () {},
    EventsEmit: function () {},
    EventsOnMultiple: function () {},
  };

  console.log('[debug-http] bridge HTTP ativo — ' + API_BASE);
})();
