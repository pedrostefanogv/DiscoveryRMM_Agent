"use strict";

(function initWindowChrome() {
  var maxBtn = document.getElementById('windowMaxBtn');
  var closeBtn = document.getElementById('windowCloseBtn');
  var metaPC = document.getElementById('windowMetaPC');
  var metaServer = document.getElementById('windowMetaServer');
  var metaConn = document.getElementById('windowMetaConn');

  function runtimeReady() {
    return !!(window.runtime && typeof window.runtime.WindowToggleMaximise === 'function');
  }

  function toggleMaximise() {
    if (!runtimeReady()) return;
    window.runtime.WindowToggleMaximise();
  }

  function hideToTray() {
    if (!runtimeReady()) return;
    window.runtime.WindowHide();
  }

  function updateWindowMeta() {
    if (document.hidden) return;
    if (!(window.go && window.go.main && window.go.main.App && typeof window.go.main.App.GetStatusOverview === 'function')) {
      return;
    }

    window.go.main.App.GetStatusOverview().then(function (status) {
      if (metaPC) metaPC.textContent = 'PC: ' + ((status && status.hostname) ? status.hostname : '-');
      if (metaServer) metaServer.textContent = 'Servidor: ' + ((status && status.server) ? status.server : '-');
      if (metaConn) metaConn.textContent = 'Conexao: ' + ((status && status.connectionType) ? status.connectionType : '-');
    }).catch(function () {
      if (metaPC) metaPC.textContent = 'PC: -';
      if (metaServer) metaServer.textContent = 'Servidor: -';
      if (metaConn) metaConn.textContent = 'Conexao: -';
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

  var windowMetaPollId = null;

  function startWindowMetaPoll() {
    stopWindowMetaPoll();
    updateWindowMeta();
    windowMetaPollId = setInterval(updateWindowMeta, 12000);
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
})();
