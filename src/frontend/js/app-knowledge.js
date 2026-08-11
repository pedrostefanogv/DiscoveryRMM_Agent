"use strict";

// Estado das páginas do artigo ativo (árvore Notion-style)
var kbActivePages = [];
var kbActivePageId = null; // null = home (artigo principal)

function showKBList() {
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
  var html = ['<div class="kb-pages-title">Páginas do artigo</div>'];
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
    appApi()
      .GetKnowledgeArticlePages(article.id)
      .then(function (pages) {
        kbActivePages = Array.isArray(pages) ? pages : [];
        kbActivePageId = null;
        renderKnowledgeArticleDetail(article);
      })
      .catch(function () {
        kbActivePages = [];
        kbActivePageId = null;
        renderKnowledgeArticleDetail(article);
      });
  }
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

async function loadKnowledgeBase() {
  if (!kbArticlesListEl) return;
  try {
    kbArticlesListEl.innerHTML =
      '<div class="meta">' +
      escapeHtml(translate("knowledge.loadingArticles")) +
      "</div>";
    knowledgeArticles = await appApi().GetKnowledgeBaseArticles();
    knowledgeArticles = Array.isArray(knowledgeArticles)
      ? knowledgeArticles
      : [];
    showKBList();
    filterKnowledgeArticles(kbSearchInputEl ? kbSearchInputEl.value : "");
  } catch (err) {
    kbArticlesListEl.innerHTML =
      '<div class="meta">' +
      escapeHtml(translate("knowledge.loadError")) +
      "</div>";
    renderKnowledgeArticleDetail(null);
  }
}

function initKnowledge() {
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
