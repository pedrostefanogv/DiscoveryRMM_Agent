"use strict";

// ---------------------------------------------------------------------------
// Tema da UI — preferência e resolução (carregado no <head> do index.html,
// ANTES dos estilos, para aplicar o tema na primeira pintura e evitar o
// "flash" claro quando o Windows/usuário está em escuro).
//
// Preferência salva em localStorage['discovery-theme']:
//   - "system" (ou ausente) -> segue o tema do dispositivo (padrão)
//   - "light"               -> força claro
//   - "dark"                -> força escuro
//
// No modo "system" o tema efetivo vem de prefers-color-scheme, que o WebView2
// espelha do Windows (AppsUseLightTheme do usuário logado) e atualiza ao vivo
// quando o usuário troca o tema do SO. O CSS continua dirigido só por
// [data-theme="dark"] em <html> — aqui só decidimos se o atributo existe.
// ---------------------------------------------------------------------------

var THEME_STORAGE_KEY = "discovery-theme";
var THEME_PREFS = ["system", "light", "dark"];

function getThemePreference() {
  var saved = null;
  try {
    saved = localStorage.getItem(THEME_STORAGE_KEY);
  } catch (_e) {
    saved = null;
  }
  return saved === "light" || saved === "dark" ? saved : "system";
}

function setThemePreference(pref) {
  if (THEME_PREFS.indexOf(pref) < 0) pref = "system";
  try {
    localStorage.setItem(THEME_STORAGE_KEY, pref);
  } catch (_e) {
    /* storage indisponível: vale só para a sessão atual */
  }
  return pref;
}

var systemDarkQuery =
  typeof window !== "undefined" && window.matchMedia
    ? window.matchMedia("(prefers-color-scheme: dark)")
    : null;

function systemPrefersDark() {
  return !!(systemDarkQuery && systemDarkQuery.matches);
}

// resolveTheme devolve o tema efetivo ("light" | "dark") de uma preferência.
function resolveTheme(pref) {
  if (pref === "light" || pref === "dark") return pref;
  return systemPrefersDark() ? "dark" : "light";
}

// applyThemePreference aplica no <html> o tema efetivo da preferência e
// devolve-o ("light" | "dark").
function applyThemePreference(pref) {
  var effective = resolveTheme(pref);
  var htmlEl = document.documentElement;
  if (effective === "dark") {
    htmlEl.setAttribute("data-theme", "dark");
  } else {
    htmlEl.removeAttribute("data-theme");
  }
  htmlEl.setAttribute("data-theme-pref", pref);
  return effective;
}

// Aplicação imediata (antes do CSS/partials).
applyThemePreference(getThemePreference());
