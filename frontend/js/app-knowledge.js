"use strict";

function renderKnowledgeArticleDetail(article) {
  if (!kbArticleDetailEl || !kbDetailTitleEl || !kbDetailMetaEl || !kbDetailContentEl) return;
  if (!article) {
    kbArticleDetailEl.classList.add('hidden');
    kbDetailTitleEl.textContent = '';
    kbDetailMetaEl.textContent = '';
    kbDetailContentEl.innerHTML = '';
    return;
  }

  kbDetailTitleEl.textContent = article.title || '-';
  kbDetailMetaEl.innerHTML = buildKnowledgeMeta(article);
  kbDetailContentEl.innerHTML = renderMarkdown(article.content || '');
  kbArticleDetailEl.classList.remove('hidden');
}

function openKnowledgeReader(article) {
  if (!kbReaderModal || !kbReaderTitleEl || !kbReaderMetaEl || !kbReaderContentEl || !article) return;

  kbReaderTitleEl.textContent = article.title || '-';
  kbReaderMetaEl.innerHTML = buildKnowledgeMeta(article);
  kbReaderContentEl.innerHTML = renderMarkdown(article.content || '');
  kbReaderModal.classList.remove('hidden');
  kbReaderModal.setAttribute('aria-hidden', 'false');
}

function closeKnowledgeReader() {
  if (!kbReaderModal) return;
  kbReaderModal.classList.add('hidden');
  kbReaderModal.setAttribute('aria-hidden', 'true');
}

function renderKnowledgeArticles(items) {
  if (!kbArticlesListEl) return;
  var list = items || [];
  if (!list.length) {
    kbArticlesListEl.innerHTML = '<div class="meta">Nenhum artigo encontrado.</div>';
    renderKnowledgeArticleDetail(null);
    return;
  }

  kbArticlesListEl.innerHTML = list.map(function (a) {
    var tags = Array.isArray(a.tags) ? a.tags : [];
    var isActive = selectedKnowledgeArticleID && selectedKnowledgeArticleID === a.id;
    return '<button class="kb-article-card ' + (isActive ? 'active' : '') + '" data-kb-id="' + escapeHtmlAttr(a.id) + '">' +
      '<span class="kb-article-title">' + escapeHtml(a.title || '-') + '</span>' +
      '<span class="kb-article-summary">' + escapeHtml(a.summary || '-') + '</span>' +
      '<span class="kb-article-badges">' +
        '<span class="kb-badge">' + escapeHtml(a.category || '-') + '</span>' +
        '<span class="kb-badge">' + escapeHtml(a.difficulty || '-') + '</span>' +
      '</span>' +
      '<span class="kb-article-tags">' + tags.map(function (t) { return '<em>#' + escapeHtml(t) + '</em>'; }).join(' ') + '</span>' +
    '</button>';
  }).join('');
}

function selectKnowledgeArticle(id) {
  if (!id) return;
  selectedKnowledgeArticleID = id;
  var article = knowledgeArticles.find(function (a) { return a.id === id; });
  renderKnowledgeArticleDetail(article || null);

  // Re-render only visual active state without changing current filter.
  var q = kbSearchInputEl ? kbSearchInputEl.value.trim() : '';
  filterKnowledgeArticles(q);
}

function filterKnowledgeArticles(query) {
  var q = String(query || '').trim().toLowerCase();
  var filtered = knowledgeArticles;
  if (q) {
    filtered = knowledgeArticles.filter(function (a) {
      var tags = Array.isArray(a.tags) ? a.tags.join(' ') : '';
      return String(a.title || '').toLowerCase().includes(q) ||
        String(a.category || '').toLowerCase().includes(q) ||
        String(a.summary || '').toLowerCase().includes(q) ||
        String(a.content || '').toLowerCase().includes(q) ||
        String(a.author || '').toLowerCase().includes(q) ||
        String(a.scope || '').toLowerCase().includes(q) ||
        String(tags).toLowerCase().includes(q);
    });
  }

  if (filtered.length && !filtered.some(function (a) { return a.id === selectedKnowledgeArticleID; })) {
    selectedKnowledgeArticleID = filtered[0].id;
  }

  renderKnowledgeArticles(filtered);
  var selected = filtered.find(function (a) { return a.id === selectedKnowledgeArticleID; });
  renderKnowledgeArticleDetail(selected || null);
}

async function loadKnowledgeBase() {
  if (!kbArticlesListEl) return;
  try {
    kbArticlesListEl.innerHTML = '<div class="meta">Carregando artigos...</div>';
    knowledgeArticles = await appApi().GetKnowledgeBaseArticles();
    knowledgeArticles = Array.isArray(knowledgeArticles) ? knowledgeArticles : [];
    if (knowledgeArticles.length && !selectedKnowledgeArticleID) {
      selectedKnowledgeArticleID = knowledgeArticles[0].id;
    }
    filterKnowledgeArticles(kbSearchInputEl ? kbSearchInputEl.value : '');
  } catch (err) {
    kbArticlesListEl.innerHTML = '<div class="meta">Erro ao carregar base de conhecimento.</div>';
    renderKnowledgeArticleDetail(null);
  }
}

function initKnowledge() {
  if (kbArticlesListEl) {
    kbArticlesListEl.addEventListener('click', function (e) {
      var btn = e.target.closest('.kb-article-card');
      if (!btn || !btn.dataset.kbId) return;
      selectKnowledgeArticle(btn.dataset.kbId);
    });
  }

  if (kbSearchInputEl) {
    kbSearchInputEl.addEventListener('input', debounce(function () {
      filterKnowledgeArticles(kbSearchInputEl.value);
    }, 250));
  }

  if (kbOpenFullBtn) {
    kbOpenFullBtn.addEventListener('click', function () {
      var article = knowledgeArticles.find(function (a) { return a.id === selectedKnowledgeArticleID; });
      if (article) openKnowledgeReader(article);
    });
  }

  if (kbReaderCloseBtn) {
    kbReaderCloseBtn.addEventListener('click', closeKnowledgeReader);
  }

  if (kbReaderModal) {
    kbReaderModal.addEventListener('click', function (e) {
      if (e.target === kbReaderModal) closeKnowledgeReader();
    });
  }
}
