"use strict";

(function () {
  var PARTIALS = [
    { mountId: "sidebarMount", path: "partials/sidebar.html" },
    { mountId: "mainMount", path: "partials/main-shell.html" },
    { mountId: "topbarMount", path: "partials/topbar.html" },
    { mountId: "statusViewMount", path: "partials/views/statusView.html" },
    { mountId: "storeViewMount", path: "partials/views/storeView.html" },
    { mountId: "updatesViewMount", path: "partials/views/updatesView.html" },
    { mountId: "inventoryViewMount", path: "partials/views/inventoryView.html" },
    { mountId: "logsViewMount", path: "partials/views/logsView.html" },
    { mountId: "chatViewMount", path: "partials/views/chatView.html" },
    { mountId: "supportViewMount", path: "partials/views/supportView.html" },
    { mountId: "knowledgeViewMount", path: "partials/views/knowledgeView.html" },
    { mountId: "automationViewMount", path: "partials/views/automationView.html" },
    { mountId: "debugViewMount", path: "partials/views/debugView.html" },
    { mountId: "psadtViewMount", path: "partials/views/psadtView.html" },
    { mountId: "p2pViewMount", path: "partials/views/p2pView.html" },
  ];

  var APP_SCRIPTS = [
    "js/app-utils.js",
    "js/app-core-globals.js",
    "js/app-window.js",
    "js/app-store-updates.js",
    "js/app-inventory.js",
    "js/app-chat.js",
    "js/app-support.js",
    "js/app-knowledge.js",
    "js/app-automation.js",
    "js/app-p2p.js",
    "js/app-status.js",
    "js/app-debug.js",
    "js/psadt-debug.js",
    "app.js",
    "js/app-init.js",
  ];

  function showBootstrapError(message) {
    var container = document.createElement("div");
    container.style.position = "fixed";
    container.style.inset = "12px";
    container.style.zIndex = "99999";
    container.style.padding = "14px";
    container.style.borderRadius = "10px";
    container.style.background = "#9a031e";
    container.style.color = "#fff";
    container.style.fontFamily = "Space Grotesk, sans-serif";
    container.style.fontSize = "14px";
    container.textContent = message;
    document.body.appendChild(container);
  }

  function loadScript(src) {
    return new Promise(function (resolve, reject) {
      var script = document.createElement("script");
      script.src = src;
      script.async = false;
      script.onload = function () { resolve(); };
      script.onerror = function () { reject(new Error("Falha ao carregar script: " + src)); };
      document.body.appendChild(script);
    });
  }

  async function loadPartial(partial) {
    var mount = document.getElementById(partial.mountId);
    if (!mount) {
      throw new Error("Mount nao encontrado: " + partial.mountId);
    }

    var response = await fetch(partial.path, { cache: "no-store" });
    if (!response.ok) {
      throw new Error("Falha ao carregar parcial: " + partial.path + " (" + response.status + ")");
    }

    mount.innerHTML = await response.text();
  }

  async function bootstrap() {
    for (var i = 0; i < PARTIALS.length; i += 1) {
      await loadPartial(PARTIALS[i]);
    }

    for (var j = 0; j < APP_SCRIPTS.length; j += 1) {
      await loadScript(APP_SCRIPTS[j]);
    }
  }

  document.addEventListener("DOMContentLoaded", function () {
    bootstrap().catch(function (error) {
      console.error(error);
      showBootstrapError("Erro ao montar UI modular: " + (error && error.message ? error.message : "desconhecido"));
    });
  });
})();
