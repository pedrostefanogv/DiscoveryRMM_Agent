"use strict";

// Estado das páginas do artigo ativo (árvore Notion-style)
var kbActivePages = [];
var kbActivePageId = null; // null = home (artigo principal)

// Token incrementado a cada render do detalhe: descarta respostas atrasadas da
// hidratação de imagens quando o usuário troca de artigo/página.
var kbRenderToken = 0;
// Cache em memória (id do anexo -> data URL) para não repetir a chamada ao Go
// a cada re-render da mesma imagem na sessão.
var kbImageDataUrlCache = new Map();
// Teto por entrada do cache acima: o Go devolve data URL de qualquer tamanho,
// mas manter as grandes na sessão inteira inflaria o renderer (base64 ~+33%).
var kbImageJsCacheMaxChars = 3 * 1024 * 1024;
// Chamadas em voo por id: várias cópias da mesma imagem na página geram UM RPC.
var kbImageInflight = new Map();

function showKBList() {
  // O zoom é fixed e ficaria aberto por cima da lista ao voltar do artigo.
  if (typeof kbZoomIsOpen === "function" && kbZoomIsOpen()) kbZoomClose();
  if (kbListViewEl) kbListViewEl.classList.remove("hidden");
  if (kbDetailViewEl) kbDetailViewEl.classList.add("hidden");
  if (kbSearchInputEl) kbSearchInputEl.classList.remove("hidden");
  if (kbStatusBarEl) kbStatusBarEl.classList.remove("hidden");
  selectedKnowledgeArticleID = null;
  kbActivePages = [];
  kbActivePageId = null;
}

function showKBDetail() {
  if (kbListViewEl) kbListViewEl.classList.add("hidden");
  if (kbDetailViewEl) kbDetailViewEl.classList.remove("hidden");
  if (kbSearchInputEl) kbSearchInputEl.classList.add("hidden");
  if (kbStatusBarEl) kbStatusBarEl.classList.add("hidden");
}

// Encontra uma página na árvore (recursivo) pelo id.
function findKbPage(pages, id) {
  for (var i = 0; i < pages.length; i++) {
    if (pages[i].id === id) return pages[i];
    var found = findKbPage(pages[i].children || [], id);
    if (found) return found;
  }
  return null;
}

// Renderiza a árvore de páginas (home + subpáginas aninhadas, estilo Notion).
function renderKbPagesNav(articleTitle) {
  if (!kbPagesNavEl) return;
  var pages = kbActivePages || [];
  if (!pages.length) {
    kbPagesNavEl.classList.add("hidden");
    kbPagesNavEl.innerHTML = "";
    return;
  }

  kbPagesNavEl.classList.remove("hidden");
  var pagesTitle = (typeof translate === "function"
    ? translate("knowledge.pagesTitle")
    : "") || "Páginas do artigo";
  var html = ['<div class="kb-pages-title">' + escapeHtml(pagesTitle) + '</div>'];
  html.push('<div class="kb-pages-tree">');

  // Home (artigo principal)
  var homeActive = kbActivePageId === null;
  html.push(
    '<button type="button" class="kb-page-item kb-page-home' +
      (homeActive ? " active" : "") +
      '" data-kb-page="__home__">' +
      '<span class="kb-page-icon">&#127968;</span>' +
      '<span class="kb-page-label">' +
      escapeHtml(articleTitle || "Início") +
      "</span></button>",
  );

  // Subpáginas aninhadas
  html.push(renderKbPagesTree(pages, 0));

  html.push("</div>");
  kbPagesNavEl.innerHTML = html.join("");
}

function renderKbPagesTree(pages, depth) {
  var html = [];
  for (var i = 0; i < pages.length; i++) {
    var p = pages[i];
    var children = p.children || [];
    var hasChildren = children.length > 0;
    var active = kbActivePageId === p.id;
    var indent = depth > 0 ? ' style="margin-left:' + depth * 14 + 'px"' : "";
    html.push(
      '<button type="button" class="kb-page-item' +
        (active ? " active" : "") +
        '" data-kb-page="' +
        escapeHtmlAttr(p.id) +
        '"' +
        indent +
        ">" +
        '<span class="kb-page-icon">' +
        (hasChildren ? "&#128230;" : "&#128196;") +
        "</span>" +
        '<span class="kb-page-label">' +
        escapeHtml(p.title || "-") +
        "</span></button>",
    );
    if (hasChildren) {
      html.push(renderKbPagesTree(children, depth + 1));
    }
  }
  return html.join("");
}

function renderKnowledgeArticleDetail(article) {
  if (
    !kbArticleDetailEl ||
    !kbDetailTitleEl ||
    !kbDetailMetaEl ||
    !kbDetailContentEl
  )
    return;
  // Novo render invalida hidratações de imagem em voo do render anterior.
  kbRenderToken++;
  // O zoom aponta para uma imagem do render anterior (o innerHTML abaixo a
  // descarta): fechar o overlay evita um lightbox órfão sobre a página nova.
  if (kbZoomIsOpen()) kbZoomClose();
  if (!article) {
    kbDetailTitleEl.textContent = "";
    kbDetailMetaEl.textContent = "";
    kbDetailContentEl.innerHTML = "";
    if (kbPagesNavEl) {
      kbPagesNavEl.classList.add("hidden");
      kbPagesNavEl.innerHTML = "";
    }
    return;
  }

  kbDetailTitleEl.textContent = article.title || "-";
  kbDetailMetaEl.innerHTML = buildKnowledgeMeta(article);

  // Conteúdo ativo: home (artigo) ou sub-página selecionada
  var activePage = kbActivePageId
    ? findKbPage(kbActivePages, kbActivePageId)
    : null;
  if (activePage) {
    kbDetailTitleEl.textContent = activePage.title || article.title || "-";
    kbDetailContentEl.innerHTML = renderMarkdown(activePage.content || "");
  } else {
    kbDetailContentEl.innerHTML = renderMarkdown(article.content || "");
  }

  renderKbPagesNav(article.title);
  syncColorMode();
  // As imagens privadas ([alt](kbimage:<id>)) precisam dos bytes vindos do Go:
  // o <img> é renderizado sem src e hidratado aqui.
  hydrateKnowledgeImages(kbDetailContentEl);
}

// hydrateKnowledgeImages busca a data URL das imagens privadas da KB via binding
// (o webview não carrega o token do agente). Usa o token do render para descartar
// respostas atrasadas e um Map em memória para não repetir a chamada.
function hydrateKnowledgeImages(container) {
  if (!container) return;
  var imgs = container.querySelectorAll("img[data-kb-image-id]");
  if (!imgs.length) return;

  var api = null;
  try {
    api = appApi();
  } catch (_) {
    api = null;
  }
  var bindingFn =
    api && typeof api.GetKnowledgeImageDataUrl === "function"
      ? api.GetKnowledgeImageDataUrl.bind(api)
      : null;

  if (!bindingFn) {
    // Binding ausente (ex.: binário antigo sem bindings regenerados). Não falha
    // silenciosamente: loga para diagnóstico e marca as imagens como indisponíveis.
    try {
      console.warn(
        "[knowledge] GetKnowledgeImageDataUrl indisponivel nos bindings. " +
          "Binário desatualizado? Use `wails3 generate bindings` e rebuild.",
      );
    } catch (_) {
      /* console indisponível */
    }
    for (var k = 0; k < imgs.length; k++) markKbImageError(imgs[k]);
    return;
  }

  var renderToken = kbRenderToken;
  for (var i = 0; i < imgs.length; i++) {
    (function (img) {
      var id = String(img.getAttribute("data-kb-image-id") || "").trim();
      if (!id) return;
      // Data URL corrompida não pode ficar "carregando" para sempre: o erro do
      // <img> cai no mesmo fallback visual das falhas de download.
      img.addEventListener(
        "error",
        function () {
          markKbImageError(img);
        },
        { once: true },
      );
      var cached = kbImageDataUrlCache.get(id);
      if (cached) {
        img.classList.remove("kb-inline-image-pending");
        img.src = cached;
        return;
      }
      // Placeholder (altura + spinner) enquanto o Go busca os bytes.
      img.classList.add("kb-inline-image-pending");
      // Dedupe in-flight: o Map só é preenchido no .then, então N cópias do
      // mesmo id na página disparariam N chamadas IPC sem isto.
      var pending = kbImageInflight.get(id);
      if (!pending) {
        pending = bindingFn(id).finally(function () {
          kbImageInflight.delete(id);
        });
        kbImageInflight.set(id, pending);
      }
      pending
        .then(function (dataUrl) {
          if (renderToken !== kbRenderToken) return; // resposta atrasada
          var value = String(dataUrl || "").trim();
          if (!value) {
            markKbImageError(img);
            return;
          }
          if (value.length <= kbImageJsCacheMaxChars) {
            kbImageDataUrlCache.set(id, value);
          }
          img.classList.remove("kb-inline-image-pending");
          img.classList.remove("kb-inline-image-error");
          img.removeAttribute("title");
          img.src = value;
        })
        .catch(function (err) {
          if (renderToken !== kbRenderToken) return;
          try {
            console.warn(
              "[knowledge] falha ao carregar imagem da KB " + id + ":",
              err,
            );
          } catch (_) {
            /* console indisponível */
          }
          markKbImageError(img);
        });
    })(imgs[i]);
  }
}

// markKbImageError remove o placeholder e sinaliza a imagem indisponível (sem
// src, o alt continua visível).
function markKbImageError(img) {
  if (!img) return;
  img.classList.remove("kb-inline-image-pending");
  img.classList.add("kb-inline-image-error");
  img.setAttribute("title", "Imagem indisponível");
  img.removeAttribute("src");
}

// ── Zoom (lightbox) das imagens da Base de Conhecimento ──────────────────
//
// O CSS já desenhava a lupa (cursor: zoom-in) em .kb-inline-image, mas NENHUM
// listener tratava o clique: o "zoom" era só uma promessa visual. Este overlay
// é dedicado à KB em vez de reaproveitar o screenshotOpenLightbox (do chat),
// porque aquele tem só o botão de fechar (sem teclado, sem estado de imagem
// indisponível) e um cabeçalho pensado para prints.

var kbZoomRetryFn = null;
// Há um retry em voo? (fora de kbZoomRetryFn para sobreviver a fechar/reabrir)
var kbZoomRetryInFlight = false;
var kbZoomView = { zoom: 1, x: 0, y: 0 };
var kbZoomMin = 1;
var kbZoomMax = 8;
var kbZoomStep = 1.25;

function kbZoomT(key) {
  return typeof translate === "function" ? translate(key) : key;
}

// kbZoomImageFromEvent devolve a imagem da KB sob o clique (ou null). O escopo
// é o container do artigo, não o documento: outras telas podem ter <img> e
// não devem abrir este overlay.
function kbZoomImageFromEvent(event) {
  var target = event.target;
  if (!target || !target.closest) return null;
  var img = target.closest("img.kb-inline-image");
  if (!img || !kbDetailContentEl || !kbDetailContentEl.contains(img)) {
    return null;
  }
  return img;
}

// kbZoomImageSrc: prefere a data URL do cache ao próprio atributo — img.src
// resolve para URL absoluta, o que mudaria o formato enviado ao overlay.
function kbZoomImageSrc(img) {
  var id = String(img.getAttribute("data-kb-image-id") || "").trim();
  if (id) {
    var cached = kbImageDataUrlCache.get(id);
    if (cached) return cached;
  }
  return img.getAttribute("src") || img.src || "";
}

function kbEnsureZoomOverlay() {
  var box = document.getElementById("kbImageZoom");
  if (box) return box;

  box = document.createElement("div");
  box.id = "kbImageZoom";
  box.className = "kb-image-zoom hidden";
  box.setAttribute("role", "dialog");
  box.setAttribute("aria-modal", "true");
  box.setAttribute("aria-label", kbZoomT("knowledge.imageZoom"));
  box.tabIndex = -1;
  box.innerHTML = [
    '<div class="kb-image-zoom-card">',
    '  <div class="kb-image-zoom-head">',
    '    <span id="kbImageZoomCaption" class="kb-image-zoom-caption"></span>',
    '    <span class="kb-image-zoom-buttons">',
    '      <span id="kbImageZoomLevel" class="kb-image-zoom-level">100%</span>',
    '      <button id="kbImageZoomOut" class="btn subtle" type="button">&#8722;</button>',
    '      <button id="kbImageZoomIn" class="btn subtle" type="button">+</button>',
    '      <button id="kbImageZoomFit" class="btn subtle" type="button">&#10530;</button>',
    '      <button id="kbImageZoomClose" class="btn danger" type="button"></button>',
    "    </span>",
    "  </div>",
    '  <div class="kb-image-zoom-stage">',
    '    <img id="kbImageZoomImg" class="kb-image-zoom-img hidden" alt="" />',
    '    <div id="kbImageZoomEmpty" class="kb-image-zoom-empty hidden">',
    '      <span id="kbImageZoomEmptyText"></span>',
    '      <button id="kbImageZoomRetry" class="btn subtle" type="button"></button>',
    "    </div>",
    "  </div>",
    '  <div class="kb-image-zoom-hint"></div>',
    "</div>",
  ].join("");
  document.body.appendChild(box);

  var closeBtn = document.getElementById("kbImageZoomClose");
  if (closeBtn) {
    closeBtn.textContent = kbZoomT("knowledge.imageClose");
    closeBtn.title = kbZoomT("knowledge.imageClose");
    closeBtn.addEventListener("click", kbZoomClose);
  }
  var hint = box.querySelector(".kb-image-zoom-hint");
  if (hint) hint.textContent = kbZoomT("knowledge.imageZoomHint");
  var inBtn = document.getElementById("kbImageZoomIn");
  if (inBtn) {
    inBtn.title = kbZoomT("screenshot.zoomIn");
    inBtn.addEventListener("click", function () {
      kbZoomApply(kbZoomView.zoom * kbZoomStep);
    });
  }
  var outBtn = document.getElementById("kbImageZoomOut");
  if (outBtn) {
    outBtn.title = kbZoomT("screenshot.zoomOut");
    outBtn.addEventListener("click", function () {
      kbZoomApply(kbZoomView.zoom / kbZoomStep);
    });
  }
  var fitBtn = document.getElementById("kbImageZoomFit");
  if (fitBtn) {
    fitBtn.title = kbZoomT("screenshot.zoomFit");
    fitBtn.addEventListener("click", kbZoomReset);
  }
  var retryBtn = document.getElementById("kbImageZoomRetry");
  if (retryBtn) {
    retryBtn.title = kbZoomT("knowledge.retry");
    retryBtn.addEventListener("click", kbZoomRetry);
  }
  var stage = box.querySelector(".kb-image-zoom-stage");
  var viewImg = document.getElementById("kbImageZoomImg");
  if (stage) {
    // Roda e arraste no quadro inteiro (não só no <img>): com a imagem ampliada
    // a área do quadro fora da imagem também responde.
    stage.addEventListener("wheel", kbZoomWheel, { passive: false });
    stage.addEventListener("mousedown", kbZoomDragStart);
    stage.addEventListener("dblclick", kbZoomReset);
  }
  if (viewImg) {
    // Soltar o src quando o overlay sai do DOM também libera a data URL cheia.
    viewImg.addEventListener("error", kbZoomViewImageError);
  }
  // Clique no fundo (fora do card) fecha.
  box.addEventListener("click", function (event) {
    if (event.target === box) kbZoomClose();
  });
  return box;
}

// kbZoomViewImageError: a imagem falhou mesmo com a data URL (arquivo
// corrompido) — mostra o estado indisponível em vez de um quadro vazio.
function kbZoomViewImageError() {
  var view = document.getElementById("kbImageZoomImg");
  var empty = document.getElementById("kbImageZoomEmpty");
  var text = document.getElementById("kbImageZoomEmptyText");
  var retry = document.getElementById("kbImageZoomRetry");
  if (!view || !empty || view.classList.contains("hidden")) return;
  view.classList.add("hidden");
  empty.classList.remove("hidden");
  if (text) text.textContent = kbZoomT("knowledge.imageUnavailable");
  if (retry) {
    retry.textContent = kbZoomT("knowledge.retry");
    retry.classList.remove("hidden");
  }
}

function kbZoomShow(box) {
  box.classList.remove("hidden");
  // A prévia do chat (hover) também é fixed e ficaria por cima do overlay.
  if (typeof screenshotHideHoverPreview === "function") {
    screenshotHideHoverPreview();
  }
  try {
    box.focus();
  } catch (_) {
    /* foco indisponível */
  }
}

function kbZoomIsOpen() {
  var box = document.getElementById("kbImageZoom");
  return !!(box && !box.classList.contains("hidden"));
}

// kbZoomOpen é o handler do clique na imagem do artigo.
function kbZoomOpen(img) {
  if (!img) return;
  var box = kbEnsureZoomOverlay();
  var view = document.getElementById("kbImageZoomImg");
  var empty = document.getElementById("kbImageZoomEmpty");
  var caption = document.getElementById("kbImageZoomCaption");
  if (!view || !empty) return;

  var alt = img.getAttribute("alt") || "";
  if (caption) caption.textContent = alt;

  var src = kbZoomImageSrc(img);
  if (!src) {
    // Hidratação falhou (offline, binário antigo…): oferece recarregar em vez
    // de abrir um overlay vazio.
    view.classList.add("hidden");
    view.removeAttribute("src");
    empty.classList.remove("hidden");
    var text = document.getElementById("kbImageZoomEmptyText");
    if (text) text.textContent = kbZoomT("knowledge.imageUnavailable");
    var retry = document.getElementById("kbImageZoomRetry");
    if (retry) {
      retry.textContent = kbZoomT("knowledge.retry");
      retry.classList.remove("hidden");
    }
    kbZoomRetryFn = function () {
      return kbZoomReloadFromSource(img);
    };
    kbZoomShow(box);
    return;
  }

  empty.classList.add("hidden");
  view.classList.remove("hidden");
  view.setAttribute("alt", alt);
  // Reabrir outra imagem não pode herdar a origem/transform de um zoom anterior
  // (o <img> do overlay é o mesmo nó entre aberturas).
  view.style.transformOrigin = "50% 50%";
  view.style.transform = "";
  view.src = src;
  kbZoomReset();
  kbZoomShow(box);
}

// kbZoomReloadFromSource refaz a hidratação de UMA imagem: descarta cache e
// chamada em voo do id e volta ao placeholder; quando chega, abre o overlay.
function kbZoomReloadFromSource(img) {
  var id = String(img.getAttribute("data-kb-image-id") || "").trim();
  var api = null;
  try {
    api = appApi();
  } catch (_) {
    api = null;
  }
  var fn =
    api && typeof api.GetKnowledgeImageDataUrl === "function"
      ? api.GetKnowledgeImageDataUrl.bind(api)
      : null;
  if (!fn || !id) return Promise.resolve("");
  kbImageDataUrlCache.delete(id);
  kbImageInflight.delete(id);
  img.classList.remove("kb-inline-image-error");
  img.classList.add("kb-inline-image-pending");
  return fn(id)
    .then(function (dataUrl) {
      var value = String(dataUrl || "").trim();
      if (!value) {
        markKbImageError(img);
        return "";
      }
      if (value.length <= kbImageJsCacheMaxChars) {
        kbImageDataUrlCache.set(id, value);
      }
      img.classList.remove("kb-inline-image-pending");
      img.classList.remove("kb-inline-image-error");
      img.removeAttribute("title");
      img.src = value;
      return value;
    })
    .catch(function () {
      markKbImageError(img);
      return "";
    });
}

function kbZoomRetry() {
  if (typeof kbZoomRetryFn !== "function") return;
  // Guard de clique repetido: sem isto N cliques disparariam N RPCs iguais
  // (o cache só é preenchido no .then). O estado fica FORA de kbZoomRetryFn:
  // fechar/reabrir recria a função e o guard precisa acompanhar apenas o RPC.
  if (kbZoomRetryInFlight) return;
  kbZoomRetryInFlight = true;
  var retryBtn = document.getElementById("kbImageZoomRetry");
  if (retryBtn) {
    retryBtn.disabled = true;
    retryBtn.textContent = kbZoomT("knowledge.loadingArticles");
  }

  var token = kbRenderToken;
  var pending = kbZoomRetryFn();
  if (!pending || typeof pending.then !== "function") {
    kbZoomRetryDone();
    return;
  }
  pending
    .then(function (src) {
      kbZoomRetryDone();
      if (!src) return;
      // O artigo/página pode ter sido trocado durante o retry: nesse caso o
      // overlay já foi fechado por renderKnowledgeArticleDetail.
      if (token !== kbRenderToken || !kbZoomIsOpen()) return;
      var view = document.getElementById("kbImageZoomImg");
      var empty = document.getElementById("kbImageZoomEmpty");
      if (!view || !empty) return;
      // Reset ANTES de atribuir o src: garante origem/transform limpos.
      kbZoomReset();
      empty.classList.add("hidden");
      view.classList.remove("hidden");
      view.src = src;
    })
    .catch(function () {
      kbZoomRetryDone();
    });
}

function kbZoomRetryDone() {
  kbZoomRetryInFlight = false;
  var retryBtn = document.getElementById("kbImageZoomRetry");
  if (retryBtn) {
    retryBtn.disabled = false;
    retryBtn.textContent = kbZoomT("knowledge.retry");
  }
}

function kbZoomClose() {
  var box = document.getElementById("kbImageZoom");
  if (!box) return;
  box.classList.add("hidden");
  var view = document.getElementById("kbImageZoomImg");
  kbZoomRetryFn = null;
  kbZoomReset();
  if (view) {
    // Soltar o src libera a memória da data URL em tamanho cheio (o
    // kbZoomReset acima não toca no src).
    view.removeAttribute("src");
    view.classList.add("hidden");
  }
  var empty = document.getElementById("kbImageZoomEmpty");
  if (empty) empty.classList.add("hidden");
  // Devolve o foco ao "Voltar" do detalhe (elemento focável de verdade): o
  // <img> só é focável com tabindex e focá-lo daria outline na imagem.
  var back = document.getElementById("kbBackBtn");
  if (back && typeof back.focus === "function") {
    try {
      back.focus({ preventScroll: true });
    } catch (_) {
      /* foco indisponível */
    }
  }
}

// kbZoomMaxZoom: nunca ampliar além de ~1 pixel original por pixel de tela
// (acima disso só borra), respeitando o teto fixo para prints pequenos.
function kbZoomMaxZoom() {
  var img = document.getElementById("kbImageZoomImg");
  if (!img || !img.clientWidth || !img.naturalWidth) return kbZoomMax;
  var natural = img.naturalWidth / img.clientWidth;
  return Math.max(kbZoomMin, Math.min(kbZoomMax, Math.round(natural * 10) / 10));
}

// kbZoomPaint aplica o transform atual em um único lugar.
function kbZoomPaint() {
  var view = kbZoomView;
  var img = document.getElementById("kbImageZoomImg");
  if (!img) return;
  img.style.transform =
    "translate(" + view.x + "px," + view.y + "px) scale(" + view.zoom + ")";
  img.style.cursor = view.zoom > 1 ? "grab" : "zoom-in";
  var level = document.getElementById("kbImageZoomLevel");
  if (level) level.textContent = Math.round(view.zoom * 100) + "%";
}

// kbZoomApply(next): zoom pelos BOTÕES/teclado (sem âncora de cursor). Zerar
// x/y evita que a posição de um arraste anterior "puxe" a imagem já ampliada.
function kbZoomApply(next) {
  var view = kbZoomView;
  var max = kbZoomMaxZoom();
  var nextZoom = Math.max(kbZoomMin, Math.min(max, next));
  if (nextZoom <= 1.001) {
    kbZoomReset();
    return;
  }
  // Ampliar/reduzir pelos botões re-centra: a origem do transform volta ao
  // centro (a roda do mouse é que troca a origem para o ponto sob o cursor).
  var img = document.getElementById("kbImageZoomImg");
  if (img) img.style.transformOrigin = "50% 50%";
  view.zoom = nextZoom;
  view.x = 0;
  view.y = 0;
  kbZoomPaint();
}

function kbZoomReset() {
  var view = kbZoomView;
  view.zoom = 1;
  view.x = 0;
  view.y = 0;
  var img = document.getElementById("kbImageZoomImg");
  if (img) {
    img.style.transformOrigin = "50% 50%";
    // Sem transform inline em 1x: evita carregar a matriz de um zoom anterior
    // para uma imagem recém-carregada (e solta o compositor).
    img.style.transform = "";
  }
  var level = document.getElementById("kbImageZoomLevel");
  if (level) level.textContent = "100%";
}

// kbZoomLimit: enquanto ampliada, a imagem pode andar até ~90% da borda — não
// dá para "perder" a imagem no arraste.
function kbZoomLimit() {
  var view = kbZoomView;
  var img = document.getElementById("kbImageZoomImg");
  if (!img) return;
  var w = img.clientWidth;
  var h = img.clientHeight;
  var maxX = Math.max(0, ((w * view.zoom) - w) / 2) * 0.9;
  var maxY = Math.max(0, ((h * view.zoom) - h) / 2) * 0.9;
  view.x = Math.max(-maxX, Math.min(maxX, view.x));
  view.y = Math.max(-maxY, Math.min(maxY, view.y));
}

// kbZoomWheel: roda do mouse dá zoom ancorado no cursor (a origem do transform
// vai para o ponto sob o mouse) e não rola a página por trás do overlay.
//
// NÃO usar kbZoomApply aqui: ele re-centra (origem 50%/50% e pan zerado), o que
// desfaria justamente a âncora de cursor calculada abaixo.
function kbZoomWheel(event) {
  if (!kbZoomIsOpen()) return;
  var img = document.getElementById("kbImageZoomImg");
  if (!img || !img.clientWidth || img.classList.contains("hidden")) return;
  event.preventDefault();

  var view = kbZoomView;
  var max = kbZoomMaxZoom();
  var factor = event.deltaY < 0 ? kbZoomStep : 1 / kbZoomStep;
  var next = Math.max(kbZoomMin, Math.min(max, view.zoom * factor));
  if (Math.abs(next - view.zoom) < 0.001) return;

  if (next <= 1.001) {
    kbZoomReset();
    return;
  }

  // Origem = ponto sob o cursor (em % da imagem JÁ transformada, via rect).
  var rect = img.getBoundingClientRect();
  if (rect.width > 0 && rect.height > 0) {
    var originX = ((event.clientX - rect.left) / rect.width) * 100;
    var originY = ((event.clientY - rect.top) / rect.height) * 100;
    img.style.transformOrigin =
      Math.max(0, Math.min(100, originX)).toFixed(2) +
      "% " +
      Math.max(0, Math.min(100, originY)).toFixed(2) +
      "%";
  }
  view.zoom = next;
  kbZoomLimit();
  kbZoomPaint();
}

// kbZoomDragStart: arrastar move a imagem ampliada (listeners no documento
// para o arraste sobreviver a sair do quadro).
function kbZoomDragStart(event) {
  if (!kbZoomIsOpen()) return;
  var view = kbZoomView;
  var img = document.getElementById("kbImageZoomImg");
  if (!img || event.button !== 0 || view.zoom <= 1) return;
  event.preventDefault();
  var startX = event.clientX;
  var startY = event.clientY;
  var x0 = view.x;
  var y0 = view.y;
  img.style.cursor = "grabbing";
  function onMove(ev) {
    view.x = x0 + (ev.clientX - startX);
    view.y = y0 + (ev.clientY - startY);
    kbZoomLimit();
    kbZoomPaint();
  }
  function onUp() {
    document.removeEventListener("mousemove", onMove);
    document.removeEventListener("mouseup", onUp);
    if (img) img.style.cursor = view.zoom > 1 ? "grab" : "zoom-in";
  }
  document.addEventListener("mousemove", onMove);
  document.addEventListener("mouseup", onUp);
}

// kbZoomKey trata os atalhos com o overlay aberto: Esc fecha, +/− ampliam,
// 0 ajusta, as setas movem e Tab fica preso no overlay. Devolve true quando
// consumiu a tecla.
function kbZoomKey(event) {
  if (!kbZoomIsOpen()) return false;
  var key = event.key;
  if (key === "Escape") {
    kbZoomClose();
    event.preventDefault();
    return true;
  }
  if (key === "Tab") {
    // aria-modal promete foco contido: sem isto o Tab iria para os controles
    // do artigo que estão escondidos atrás do backdrop. A lista é só de
    // elementos realmente focáveis (button/link/input/[tabindex]), não de
    // qualquer coisa com classe .btn.
    var box = document.getElementById("kbImageZoom");
    var candidates = box
      ? box.querySelectorAll(
          "button, [href], input, select, textarea, [tabindex]:not([tabindex='-1'])",
        )
      : [];
    var list = Array.prototype.filter.call(candidates, function (el) {
      return (
        !el.disabled &&
        !el.classList.contains("hidden") &&
        el.tabIndex !== -1
      );
    });
    if (!list.length) return false;
    var first = list[0];
    var last = list[list.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      last.focus();
      event.preventDefault();
      return true;
    }
    if (!event.shiftKey && document.activeElement === last) {
      first.focus();
      event.preventDefault();
      return true;
    }
    return false;
  }
  if (key === "+" || key === "=") {
    kbZoomApply(kbZoomView.zoom * kbZoomStep);
    event.preventDefault();
    return true;
  }
  if (key === "-" || key === "_") {
    kbZoomApply(kbZoomView.zoom / kbZoomStep);
    event.preventDefault();
    return true;
  }
  if (key === "0") {
    kbZoomReset();
    event.preventDefault();
    return true;
  }
  var arrows = {
    ArrowLeft: [-1, 0],
    ArrowRight: [1, 0],
    ArrowUp: [0, -1],
    ArrowDown: [0, 1],
  };
  if (!arrows[key] || kbZoomView.zoom <= 1) return false;
  var step = event.shiftKey ? 80 : 30;
  kbZoomView.x += arrows[key][0] * step;
  kbZoomView.y += arrows[key][1] * step;
  kbZoomLimit();
  kbZoomPaint();
  event.preventDefault();
  return true;
}

// initKnowledgeZoom liga o clique na imagem (o "zoom-in" do cursor só existia
// no CSS), o teclado do overlay e o reenquadramento quando a janela muda de
// tamanho (em 1x o card é centrado pelo CSS, então o reset basta).
function initKnowledgeZoom() {
  if (!kbDetailContentEl) return;
  kbDetailContentEl.addEventListener("click", function (event) {
    var img = kbZoomImageFromEvent(event);
    if (!img) return;
    // img.kb-inline-image-error não tem src: o overlay mostra "indisponível"
    // com ação de recarregar (antes o clique não fazia nada).
    event.preventDefault();
    kbZoomOpen(img);
  });
  document.addEventListener("keydown", function (event) {
    // Outro handler já tratou a tecla (Esc de outro modal, atalho global).
    if (event.defaultPrevented) return;
    if (!kbZoomIsOpen()) return;
    kbZoomKey(event);
  });
  window.addEventListener("resize", function () {
    if (!kbZoomIsOpen()) return;
    // No estado "indisponível" não há imagem para reenquadrar.
    var view = document.getElementById("kbImageZoomImg");
    if (!view || view.classList.contains("hidden")) return;
    kbZoomReset();
  });
}

function renderKnowledgeArticles(items) {
  if (!kbArticlesListEl) return;
  var list = items || [];
  if (!list.length) {
    kbArticlesListEl.innerHTML =
      '<div class="kb-empty-state">' +
      '<div class="kb-empty-icon">&#128218;</div>' +
      '<div class="kb-empty-text">' +
      escapeHtml(translate("knowledge.noArticlesFound")) +
      "</div>" +
      "</div>";
    return;
  }

  // Calcula o nível de profundidade de cada artigo (0 = raiz) a partir de parentId
  var depthMap = {};
  var byId = {};
  list.forEach(function (a) {
    byId[a.id] = a;
  });
  function depthOf(a) {
    if (depthMap[a.id] !== undefined) return depthMap[a.id];
    var d = 0;
    var cur = a;
    var guard = 0;
    while (cur && cur.parentId && byId[cur.parentId] && guard < 4) {
      d++;
      cur = byId[cur.parentId];
      guard++;
    }
    depthMap[a.id] = d;
    return d;
  }

  kbArticlesListEl.innerHTML = list
    .map(function (a) {
      var tags = Array.isArray(a.tags) ? a.tags : [];
      var cat = String(a.category || "").trim();
      var diff = String(a.difficulty || "").trim();
      var badges = "";
      if (cat && cat !== "-")
        badges += '<span class="kb-badge">' + escapeHtml(cat) + "</span>";
      if (diff && diff !== "-")
        badges +=
          '<span class="kb-badge kb-badge-scope">' +
          escapeHtml(diff) +
          "</span>";
      var kbVersion = Number(a.currentVersionNumber || 0);
      if (kbVersion > 0)
        badges +=
          '<span class="kb-badge" title="Versão do artigo">v' +
          escapeHtml(String(kbVersion)) +
          "</span>";
      var tagsHtml = tags
        .map(function (t) {
          return "<em>#" + escapeHtml(t) + "</em>";
        })
        .join(" ");
      var depth = depthOf(a);
      var indent = depth > 0 ? ' style="margin-left:' + depth * 16 + 'px"' : "";
      var icon = a.isPage
        ? '<span class="kb-tree-icon">&#128193;</span>'
        : depth > 0
          ? '<span class="kb-tree-icon">&#128196;</span>'
          : "";
      return (
        '<button class="kb-article-card" data-kb-id="' +
        escapeHtmlAttr(a.id) +
        '"' +
        indent +
        ">" +
        icon +
        '<span class="kb-article-title">' +
        escapeHtml(a.title || "-") +
        "</span>" +
        (a.summary
          ? '<span class="kb-article-summary">' +
            escapeHtml(a.summary) +
            "</span>"
          : "") +
        (badges
          ? '<span class="kb-article-badges">' + badges + "</span>"
          : "") +
        (tagsHtml
          ? '<span class="kb-article-tags">' + tagsHtml + "</span>"
          : "") +
        "</button>"
      );
    })
    .join("");
}

function selectKnowledgeArticle(id) {
  if (!id) return;
  selectedKnowledgeArticleID = id;
  var article = knowledgeArticles.find(function (a) {
    return a.id === id;
  });
  kbActivePages = [];
  kbActivePageId = null;
  renderKnowledgeArticleDetail(article || null);
  showKBDetail();

  // Busca as sub-páginas do artigo (árvore Notion-style)
  if (article && article.id) {
    var requestedId = article.id;
    var api = appApi();
    var bindingFn = api && typeof api.GetKnowledgeArticlePages === "function"
      ? api.GetKnowledgeArticlePages.bind(api)
      : null;

    if (!bindingFn) {
      // Binding ausente (ex.: binário antigo sem bindings regenerados).
      // NÃO falha silenciosamente: loga para diagnóstico e segue sem a árvore.
      try {
        console.warn(
          "[knowledge] GetKnowledgeArticlePages indisponivel nos bindings. " +
            "Binário desatualizado? Use `wails3 generate bindings` e rebuild.",
        );
      } catch (_) {
        /* console indisponível */
      }
      if (requestedId === selectedKnowledgeArticleID) {
        kbActivePages = [];
        kbActivePageId = null;
        renderKnowledgeArticleDetail(article);
      }
      return;
    }

    bindingFn(requestedId)
      .then(function (pages) {
        // Guard de race: se o usuário já trocou de artigo, ignora a resposta atrasada
        if (requestedId !== selectedKnowledgeArticleID) return;
        kbActivePages = Array.isArray(pages) ? pages : [];
        kbActivePageId = null;
        renderKnowledgeArticleDetail(article);
      })
      .catch(function (err) {
        try {
          console.warn(
            "[knowledge] falha ao carregar paginas do artigo " +
              requestedId +
              ":",
            err,
          );
        } catch (_) {
          /* console indisponível */
        }
        if (requestedId !== selectedKnowledgeArticleID) return;
        kbActivePages = [];
        kbActivePageId = null;
        renderKnowledgeArticleDetail(article);
      });
  }
}

// Abre um artigo especifico por id (usado pelo deep link discovery://knowledge/article/<id>
// vindo dos cards do chat). Garante que a lista esteja carregada antes de selecionar.
async function openKnowledgeArticleById(id) {
  var target = String(id || "").trim();
  if (!target) return false;

  var article = knowledgeArticles.find(function (a) {
    return a && a.id === target;
  });

  if (!article) {
    await loadKnowledgeBase();
    article = knowledgeArticles.find(function (a) {
      return a && a.id === target;
    });
  }

  if (!article) return false;

  selectKnowledgeArticle(target);
  return true;
}

function selectKbPage(pageId) {
  kbActivePageId = pageId === "__home__" ? null : pageId;
  var article = knowledgeArticles.find(function (a) {
    return a.id === selectedKnowledgeArticleID;
  });
  renderKnowledgeArticleDetail(article || null);
}

function filterKnowledgeArticles(query) {
  var q = String(query || "")
    .trim()
    .toLowerCase();
  var filtered = knowledgeArticles;
  if (q) {
    filtered = knowledgeArticles.filter(function (a) {
      var tags = Array.isArray(a.tags) ? a.tags.join(" ") : "";
      return (
        String(a.title || "")
          .toLowerCase()
          .includes(q) ||
        String(a.category || "")
          .toLowerCase()
          .includes(q) ||
        String(a.summary || "")
          .toLowerCase()
          .includes(q) ||
        String(a.content || "")
          .toLowerCase()
          .includes(q) ||
        String(a.author || "")
          .toLowerCase()
          .includes(q) ||
        String(a.scope || "")
          .toLowerCase()
          .includes(q) ||
        String(tags).toLowerCase().includes(q)
      );
    });
  }

  renderKnowledgeArticles(filtered);
  // When searching, always switch back to list view
  showKBList();
}

var kbLoading = false;

// Aviso de modo offline com a data do backup (quando disponível).
function renderKnowledgeCacheState(result) {
  var banner = document.getElementById("kbStaleBanner");
  if (!banner) return;
  if (!(result && result.stale)) {
    banner.classList.add("hidden");
    banner.textContent = "";
    return;
  }
  var msg = translate("knowledge.staleCache");
  var when = result.cachedAt ? String(result.cachedAt) : "";
  if (when && typeof formatDate === "function") {
    try { when = formatDate(result.cachedAt, "") || when; } catch (_) { /* valor cru */ }
  }
  if (when) msg += " " + translate("cache.updatedAt", { time: when });
  banner.textContent = msg;
  banner.classList.remove("hidden");
}

async function loadKnowledgeBase() {
  if (!kbArticlesListEl || kbLoading) return; // guard: evita cargas concorrentes
  kbLoading = true;
  var staleBanner = document.getElementById("kbStaleBanner");
  try {
    kbArticlesListEl.innerHTML =
      '<div class="meta">' +
      escapeHtml(translate("knowledge.loadingArticles")) +
      "</div>";
    // Binding com status propaga erro (habilita o estado de erro/retry) e
    // informa se os artigos vieram do backup offline.
    var api = appApi();
    if (typeof api.GetKnowledgeBaseArticlesWithStatus === "function") {
      var result = await api.GetKnowledgeBaseArticlesWithStatus();
      knowledgeArticles = result && Array.isArray(result.articles)
        ? result.articles
        : (Array.isArray(result) ? result : []);
      renderKnowledgeCacheState(result);
    } else {
      knowledgeArticles = await api.GetKnowledgeArticles("");
      renderKnowledgeCacheState(null);
    }
    knowledgeArticles = Array.isArray(knowledgeArticles)
      ? knowledgeArticles
      : [];
    showKBList();
    filterKnowledgeArticles(kbSearchInputEl ? kbSearchInputEl.value : "");
  } catch (err) {
    if (staleBanner) staleBanner.classList.add("hidden");
    // Estado de erro distinguível de 'sem artigos', com ação de retry
    // embutida — antes o usuário via só um texto sem caminho de recuperação.
    kbArticlesListEl.innerHTML =
      '<div class="kb-empty-state">' +
      '<div class="kb-empty-icon">&#9888;</div>' +
      '<div class="kb-empty-text">' +
      escapeHtml(translate("knowledge.loadError")) +
      "</div>" +
      '<button type="button" class="btn subtle kb-retry-btn" id="kbRetryBtn">' +
      escapeHtml(translate("knowledge.retry")) +
      "</button>" +
      "</div>";
    var retryBtn = document.getElementById("kbRetryBtn");
    if (retryBtn) {
      retryBtn.addEventListener("click", loadKnowledgeBase);
    }
    renderKnowledgeArticleDetail(null);
  } finally {
    kbLoading = false;
  }
}

function initKnowledge() {
  initKnowledgeZoom();
  if (kbRefreshBtn) {
    kbRefreshBtn.addEventListener("click", async function () {
      kbRefreshBtn.disabled = true;
      try {
        await appApi().RefreshKnowledgeBase();
      } catch (_) {
        /* ignora erro do refresh e tenta recarregar mesmo assim */
      }
      await loadKnowledgeBase();
      kbRefreshBtn.disabled = false;
    });
  }

  if (kbArticlesListEl) {
    kbArticlesListEl.addEventListener("click", function (e) {
      var btn = e.target.closest(".kb-article-card");
      if (!btn || !btn.dataset.kbId) return;
      selectKnowledgeArticle(btn.dataset.kbId);
    });
  }

  if (kbPagesNavEl) {
    kbPagesNavEl.addEventListener("click", function (e) {
      var btn = e.target.closest(".kb-page-item");
      if (!btn || !btn.dataset.kbPage) return;
      selectKbPage(btn.dataset.kbPage);
    });
  }

  if (kbBackBtn) {
    kbBackBtn.addEventListener("click", function () {
      showKBList();
    });
  }

  if (kbSearchInputEl) {
    kbSearchInputEl.addEventListener(
      "input",
      debounce(function () {
        filterKnowledgeArticles(kbSearchInputEl.value);
      }, 250),
    );
  }
}
