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

  // Nome do evento em que o Go avisa que a sessão do overlay terminou (mesmo
  // emitido por releaseScreenshotOverlay em screenshot.go: "screenshot:overlay_close").
  var SCREENSHOT_OVERLAY_CLOSE_EVENT = "screenshot:overlay_close";
  // Listener já registrado? O boot pode rodar mais de uma vez em hot-reload.
  var backendCloseBound = false;

  function boot() {
    // Handshake: o Go espera este aviso para exibir a janela (sem ele o usuário
    // veria um retângulo vazio enquanto a página carrega).
    call("ScreenshotOverlayReady").catch(function () {});
    bindBackendClose();
    loop();
  }

  // bindBackendClose escuta o encerramento REAL da sessão no backend.
  //
  // Antes só existia o laço que avança quando screenshotOverlayState vira
  // null. Se o Go encerrasse a sessão sozinho (abort, erro no Finish, janela
  // fechada/ocultada), o estado local ficava preso com a tela congelada antiga:
  // o laço nunca puxava a captura seguinte e o usuário só saía apertando Esc —
  // aí a próxima captura reexibia a imagem velha.
  function bindBackendClose() {
    if (backendCloseBound) return;
    if (!window.wails || typeof window.wails.on !== "function") return;
    backendCloseBound = true;
    try {
      window.wails.on(SCREENSHOT_OVERLAY_CLOSE_EVENT, onBackendClose);
    } catch (err) {
      backendCloseBound = false;
      if (window.console && typeof console.warn === "function") {
        console.warn("[overlay] não foi possível escutar o fim da sessão:", err);
      }
    }
  }

  // onBackendClose limpa o estado local quando o backend encerra a sessão.
  // Evento de OUTRA sessão (captura já substituída) é ignorado.
  function onBackendClose(raw) {
    var info = parsePayload(raw);
    var state = screenshotOverlayState;
    if (!state) return;
    if (info && info.session && state.payload && state.payload.session &&
        String(info.session) !== String(state.payload.session)) {
      return;
    }
    screenshotHideOverlay();
  }

  // clearStaleOverlay limpa um estado local que o backend não reconhece mais.
  // Chamado quando ScreenshotOverlayNext responde VAZIO (sem sessão/timeout) —
  // sinal de que não há captura pendente e o laço não deve seguir exibindo a
  // tela congelada de uma sessão já encerrada.
  function clearStaleOverlay() {
    if (!screenshotOverlayState) return;
    screenshotHideOverlay();
  }

  function loop() {
    call("ScreenshotOverlayNext")
      .then(function (raw) {
        var payload = parsePayload(raw);
        if (!payload || !payload.session) {
          clearStaleOverlay();
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
        // Binding indisponível/erro de transporte: sem overlay desenhado não há o
        // que limpar; só tenta de novo.
        setTimeout(loop, 800);
      });
  }

  // screenshotHideOverlay (Finish/Cancel/evento de fechamento do Go) zera
  // screenshotOverlayState; quando isso acontece a janela já foi escondida pelo
  // Go e pode puxar a próxima captura.
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
