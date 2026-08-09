"use strict";

// ─────────────────────────────────────────────────────────────────────────────
// Wails v3 Bridge — bindings nativos + runtime nativo estruturado
// ─────────────────────────────────────────────────────────────────────────────
// Importa DIRETAMENTE os bindings nativos v3 (gerados por `wails3 generate
// bindings`) e o runtime nativo (`/wails/runtime.js`).
//
// Expõe:
//   - window.go.app.App  → bindings dos métodos Go (Service App)
//   - window.wails       → runtime v3 estruturado (Events, Browser, Window)
//
// O frontend usa scripts clássicos (não ES modules), então este bridge é o
// ponto único de integração. Deve ser carregado como `<script type="module">`
// ANTES do `bootstrap-partials.js`.
//
// Helpers de conveniência sob window.wails:
//   - wails.on(name, cb)      → Events.On com unwrap WailsEvent→data
//   - wails.openURL(url)      → Browser.OpenURL
//   - wails.toggleMaximise()  → Window.ToggleMaximise
//   - wails.hideWindow()      → Window.Hide
// ─────────────────────────────────────────────────────────────────────────────

import * as App from "./bindings/discovery/app/app.js";
import { Events, Browser, Window } from "/wails/runtime.js";

// ── window.go.app.App ────────────────────────────────────────────────────────
// Expõe os bindings nativos v3 sob `window.go.app.App`.
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

// ── window.wails (runtime v3 estruturado) ───────────────────────────────────
// Expõe o runtime nativo v3 de forma estruturada para os scripts clássicos.
// Só define se ainda não existir (não sobrescreve o debug-http-bridge no
// modo navegador, que injeta seu próprio window.wails via SSE).
(function exposeWailsRuntime() {
  if (window.wails) {
    return;
  }

  // Helper de conveniência: registra listener de evento com unwrap automático
  // do WailsEvent → dado cru (compat com handlers existentes que esperam o
  // dado diretamente, não o objeto WailsEvent).
  function on(eventName, callback) {
    return Events.On(eventName, function (ev) {
      callback(ev && ev.data !== undefined ? ev.data : ev);
    });
  }

  window.wails = {
    // Módulos nativos v3 (para uso direto quando necessário).
    Events: Events,
    Browser: Browser,
    Window: Window,

    // Helpers de conveniência (API simplificada).
    on: on,
    emit: function (name) { return Events.Emit.apply(Events, arguments); },
    openURL: function (url) { return Browser.OpenURL(url); },
    toggleMaximise: function () { return Window.ToggleMaximise(); },
    hideWindow: function () { return Window.Hide(); },
  };
})();

// Sinaliza que o bridge v3 foi carregado (útil para debug).
window.__wailsV3Bridge = true;
console.log("[wails-v3] bridge de bindings nativos carregado");
