"use strict";

(function initWindowChrome() {
  var maxBtn = document.getElementById('windowMaxBtn');
  var closeBtn = document.getElementById('windowCloseBtn');
  var metaPCName = document.getElementById('windowMetaPCName');
  var metaDot = document.getElementById('windowMetaDot');

  function runtimeReady() {
    return !!(window.wails && typeof window.wails.toggleMaximise === 'function');
  }

  function toggleMaximise() {
    if (!runtimeReady()) return;
    window.wails.toggleMaximise();
  }

  function hideToTray() {
    if (!runtimeReady()) return;
    window.wails.hideWindow();
  }

  function updateWindowMeta() {
    if (document.hidden) return;
    if (!(window.go && window.go.app && window.go.app.App && typeof window.go.app.App.GetStatusOverview === 'function')) {
      return;
    }

    window.go.app.App.GetStatusOverview().then(function (status) {
      var hostname = (status && status.hostname) ? status.hostname : '-';
      if (metaPCName) metaPCName.textContent = translate('window.meta.pc') + ': ' + hostname;
      if (metaDot) {
        var online = !!(status && status.connected);
        metaDot.classList.toggle('online', online);
        metaDot.classList.toggle('offline', !online);
      }
    }).catch(function () {
      if (metaPCName) metaPCName.textContent = translate('window.meta.pc') + ': -';
      if (metaDot) {
        metaDot.classList.add('offline');
        metaDot.classList.remove('online');
      }
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
