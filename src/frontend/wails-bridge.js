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
//
// IMPORTANTE: o runtime nativo do Wails v3 JÁ define `window.wails` com os
// módulos padrão (`index_exports`: Events, Browser, Window, ...) quando
// `/wails/runtime.js` é avaliado. Como este bridge é um ES module que importa
// esse runtime, `window.wails` já existe quando este corpo roda. Portanto NÃO
// podemos retornar cedo se `window.wails` existir — senão os helpers de
// conveniência (`toggleMaximise`, `hideWindow`, `on`, `emit`, `openURL`) nunca
// seriam adicionados e os botões de janela do frontend (que checam
// `window.wails.toggleMaximise`) parariam de funcionar.
//
// Estratégia: MESCLAR os helpers sobre o objeto existente, sem sobrescrever os
// módulos nativos. Só define os módulos caso `window.wails` não exista ainda
// (ex.: modo navegador sem o bridge HTTP, que injeta seu próprio window.wails).
(function exposeWailsRuntime() {
  // Helper de conveniência: registra listener de evento com unwrap automático
  // do WailsEvent → dado cru (compat com handlers existentes que esperam o
  // dado diretamente, não o objeto WailsEvent).
  function on(eventName, callback) {
    return Events.On(eventName, function (ev) {
      callback(ev && ev.data !== undefined ? ev.data : ev);
    });
  }

  window.wails = window.wails || {};

  // Garante os módulos nativos (já presentes quando o runtime nativo rodou).
  window.wails.Events = window.wails.Events || Events;
  window.wails.Browser = window.wails.Browser || Browser;
  window.wails.Window = window.wails.Window || Window;

  // Helpers de conveniência (API simplificada usada pelos scripts clássicos).
  // `||` preserva implementações já existentes (ex.: do debug-http-bridge
  // no modo navegador), se houver.
  window.wails.on = window.wails.on || on;
  window.wails.emit = window.wails.emit || function (name) {
    return Events.Emit.apply(Events, arguments);
  };
  window.wails.openURL = window.wails.openURL || function (url) {
    return Browser.OpenURL(url);
  };
  window.wails.toggleMaximise = window.wails.toggleMaximise || function () {
    return Window.ToggleMaximise();
  };
  window.wails.hideWindow = window.wails.hideWindow || function () {
    return Window.Hide();
  };
})();

// Sinaliza que o bridge v3 foi carregado (útil para debug).
window.__wailsV3Bridge = true;
console.log("[wails-v3] bridge de bindings nativos carregado");
