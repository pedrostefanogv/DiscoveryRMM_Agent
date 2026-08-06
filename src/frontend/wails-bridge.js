"use strict";

// ─────────────────────────────────────────────────────────────────────────────
// Wails v3 Bridge — bindings nativos + runtime nativo
// ─────────────────────────────────────────────────────────────────────────────
// Substitui o antigo `wails-v3-shim.js`. Em vez de reconstruir manualmente a
// API do Wails v2, este bridge importa DIRETAMENTE os bindings nativos v3
// (gerados por `wails3 generate bindings`) e o runtime nativo (`/wails/runtime.js`).
//
// O frontend do Discovery ainda usa scripts clássicos (não ES modules), então
// expomos os bindings nativos sob `window.go.app.App` e um `window.runtime`
// MÍNIMO (apenas os métodos realmente usados pelos scripts), que delega para
// o runtime nativo v3.
//
// Este arquivo DEVE ser carregado como `<script type="module">` ANTES do
// `bootstrap-partials.js`, pois os bindings v3 são módulos ES.
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

// ── window.runtime (mínimo, delegando ao runtime nativo v3) ─────────────────
// O frontend usa `window.runtime.EventsOn(name, cb)` onde `cb` recebe o dado
// diretamente. No v3, o callback de `Events.On` recebe um objeto `WailsEvent`
// com `.data`. Adaptamos apenas os métodos efetivamente usados.
// Só define se ainda não existir (não sobrescreve o debug-http-bridge).
(function exposeRuntime() {
  if (window.runtime && window.runtime.EventsOn) {
    return;
  }
  window.runtime = window.runtime || {};

  // Eventos: unwrap do WailsEvent → dado cru (compat com handlers existentes).
  window.runtime.EventsOn = function (eventName, callback) {
    return Events.On(eventName, function (ev) {
      callback(ev && ev.data !== undefined ? ev.data : ev);
    });
  };

  // Navegador.
  window.runtime.BrowserOpenURL = function (url) {
    return Browser.OpenURL(url);
  };

  // Janela (usado pelo chrome da janela frameless).
  window.runtime.WindowToggleMaximise = function () { return Window.ToggleMaximise(); };
  window.runtime.WindowHide = function () { return Window.Hide(); };
})();

// Sinaliza que o bridge v3 foi carregado (útil para debug).
window.__wailsV3Bridge = true;
console.log("[wails-v3] bridge de bindings nativos carregado");
