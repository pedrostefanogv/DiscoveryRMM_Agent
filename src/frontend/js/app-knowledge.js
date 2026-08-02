"use strict";

function showKBList() {
  if (kbListViewEl) kbListViewEl.classList.remove("hidden");
  if (kbDetailViewEl) kbDetailViewEl.classList.add("hidden");
  if (kbSearchInputEl) kbSearchInputEl.classList.remove("hidden");
  selectedKnowledgeArticleID = null;
}

function showKBDetail() {
  if (kbListViewEl) kbListViewEl.classList.add("hidden");
  if (kbDetailViewEl) kbDetailViewEl.classList.remove("hidden");
  if (kbSearchInputEl) kbSearchInputEl.classList.add("hidden");
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
    return;
  }

  kbDetailTitleEl.textContent = article.title || "-";
  kbDetailMetaEl.innerHTML = buildKnowledgeMeta(article);
  kbDetailContentEl.innerHTML = renderMarkdown(article.content || "");
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
      return (
        '<button class="kb-article-card" data-kb-id="' +
        escapeHtmlAttr(a.id) +
        '">' +
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
  renderKnowledgeArticleDetail(article || null);
  showKBDetail();
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
