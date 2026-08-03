"use strict";

// ─────────────────────────────────────────────────────────────────────────────
// Wails v3 Compatibility Shim
// ─────────────────────────────────────────────────────────────────────────────
// O frontend do Discovery foi construído para a API do Wails v2:
//   - `window.go.app.App.<method>(...)`  → chamadas de binding
//   - `window.runtime.EventsOn/EventsOff/EventsEmit(...)` → eventos
//
// No Wails v3, os bindings são ES modules que importam de `/wails/runtime.js`
// e o runtime expõe `window.wails`. Este shim reconstrói a API v2 por cima
// dos bindings v3, para que o frontend existente continue funcionando sem
// reescrita massiva.
//
// Este arquivo DEVE ser carregado como `<script type="module">` ANTES do
// `bootstrap-partials.js`, pois os bindings v3 são módulos ES.
// ─────────────────────────────────────────────────────────────────────────────

import * as App from "./bindings/discovery/app/app.js";
import { Events, Browser, Window } from "/wails/runtime.js";

// ── window.go.app.App ────────────────────────────────────────────────────────
// Expõe todos os métodos do binding v3 sob `window.go.app.App`.
// O frontend chama `appApi()` que retorna `window.go.app.App`.
// Só define se ainda não existir (não sobrescreve o debug-http-bridge no
// modo navegador, que injeta um proxy HTTP).
(function exposeGoBindings() {
  if (window.go && window.go.app && window.go.app.App) {
    return;
  }
  window.go = window.go || {};
  window.go.app = window.go.app || {};
  window.go.app.App = App;
})();

// ── window.runtime (compat v2) ───────────────────────────────────────────────
// O frontend usa `window.runtime.EventsOn(name, cb)` onde `cb` recebe o dado
// diretamente (não um objeto WailsEvent). Adaptamos para a API v3.
// Só define se ainda não existir (não sobrescreve o debug-http-bridge).
(function exposeRuntime() {
  if (window.runtime && window.runtime.EventsOn) {
    return;
  }
  window.runtime = window.runtime || {};

  window.runtime.EventsOn = function (eventName, callback) {
    return Events.On(eventName, function (ev) {
      // v3 entrega um objeto WailsEvent { name, data, sender }.
      // O frontend v2 espera o dado direto.
      callback(ev && ev.data !== undefined ? ev.data : ev);
    });
  };

  window.runtime.EventsOnMultiple = function (eventName, callback, maxCallbacks) {
    return Events.OnMultiple(eventName, function (ev) {
      callback(ev && ev.data !== undefined ? ev.data : ev);
    }, maxCallbacks);
  };

  window.runtime.EventsOff = function (eventName) {
    Events.Off(eventName);
  };

  window.runtime.EventsOffAll = function () {
    Events.OffAll();
  };

  window.runtime.EventsEmit = function (eventName, ...data) {
    Events.Emit(eventName, ...data);
  };

  // Log helpers (usados pelo frontend em alguns pontos).
  window.runtime.LogPrint = function (msg) { console.log("[wails]", msg); };
  window.runtime.LogInfo = function (msg) { console.info("[wails]", msg); };
  window.runtime.LogDebug = function (msg) { console.debug("[wails]", msg); };
  window.runtime.LogWarning = function (msg) { console.warn("[wails]", msg); };
  window.runtime.LogError = function (msg) { console.error("[wails]", msg); };
  window.runtime.LogFatal = function (msg) { console.error("[wails][FATAL]", msg); };
})();

// Sinaliza que o shim v3 foi carregado (útil para debug).
window.__wailsV3Shim = true;
console.log("[wails-v3] shim de compatibilidade carregado");
