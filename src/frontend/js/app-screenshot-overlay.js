"use strict";

// ── Host DEDICADO do overlay de captura (screenshot-overlay.html) ───────────
//
// A janela do overlay é criada sob demanda pelo Go (screenshot_overlay_window.go),
// cobre o desktop virtual e desenha a seleção aqui — sem depender da janela
// principal do agent (que fica no tray enquanto o usuário conversa no chat de
// suporte). O payload da tela congelada é PUXADO por ScreenshotOverlayNext: a
// janela espera a próxima captura em laço, o que elimina a corrida de um evento
// emitido antes de o handler existir.

window.__SCREENSHOT_OVERLAY_HOST__ = true;

// app-screenshot.js espera estas duas funções do host (na UI principal elas vêm
// de app-core-globals.js). Aqui a janela é mínima: sem appApi os bindings do
// Wails não são alcançáveis e nada funcionaria, então devolvemos null e o
// módulo trata como "captura indisponível".
function appApi() {
  if (!window.go || !window.go.app || !window.go.app.App) return null;
  return window.go.app.App;
}

function showFeedback(message) {
  if (window.console && typeof console.warn === "function") {
    console.warn("[overlay] " + String(message || ""));
  }
}

(function () {
  // screenshotNextPollDelayMs cobre o caso "sem sessão": a janela fica viva
  // entre capturas e volta a perguntar ao Go.
  var screenshotNextPollDelayMs = 400;

  function call(method) {
    var args = Array.prototype.slice.call(arguments, 1);
    var target = appApi();
    if (!target || typeof target[method] !== "function") {
      return Promise.reject(new Error("Binding indisponível: " + method));
    }
    try {
      return Promise.resolve(target[method].apply(target, args));
    } catch (err) {
      return Promise.reject(err);
    }
  }

  function parsePayload(raw) {
    if (typeof raw === "string") {
      try {
        return JSON.parse(raw);
      } catch (_) {
        return null;
      }
    }
    return raw || null;
  }

  function boot() {
    // Handshake: o Go espera este aviso para exibir a janela (sem ele o usuário
    // veria um retângulo vazio enquanto a página carrega).
    call("ScreenshotOverlayReady").catch(function () {});
    loop();
  }

  function loop() {
    call("ScreenshotOverlayNext")
      .then(function (raw) {
        var payload = parsePayload(raw);
        if (!payload || !payload.session) {
          setTimeout(loop, screenshotNextPollDelayMs);
          return;
        }
        screenshotOverlayDestination = String(payload.destination || "aiChat");
        try {
          screenshotShowOverlay(payload);
        } catch (err) {
          if (window.console && typeof console.error === "function") {
            console.error("[overlay] falha ao desenhar a captura:", err);
          }
          // Sem overlay desenhado a sessão ficaria presa cobrindo o desktop:
          // encerra no backend (o restore devolve as janelas do agent).
          screenshotCancelBackendSession(payload.session);
          setTimeout(loop, screenshotNextPollDelayMs);
          return;
        }
        waitSessionEnd(loop);
      })
      .catch(function () {
        setTimeout(loop, 800);
      });
  }

  // screenshotHideOverlay (Finish/Cancel) zera screenshotOverlayState; quando
  // isso acontece a janela já foi escondida pelo Go e pode puxar a próxima.
  function waitSessionEnd(next) {
    var timer = setInterval(function () {
      if (screenshotOverlayState) return;
      clearInterval(timer);
      next();
    }, 250);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", boot);
  } else {
    boot();
  }
})();
