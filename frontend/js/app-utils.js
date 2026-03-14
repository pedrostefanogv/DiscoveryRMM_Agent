"use strict";

function debounce(fn, delayMs) {
  var timeoutId;
  return function () {
    var ctx = this;
    var args = arguments;
    clearTimeout(timeoutId);
    timeoutId = setTimeout(function () { fn.apply(ctx, args); }, delayMs);
  };
}

function getPaginationState(items, currentPage, pageSize) {
  var totalPages = Math.max(1, Math.ceil(items.length / pageSize));
  var validPage = Math.max(1, Math.min(currentPage, totalPages));
  var start = (validPage - 1) * pageSize;
  return { totalPages: totalPages, validPage: validPage, start: start };
}

function escapeHtml(value) {
  return String(value)
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#039;');
}

function escapeHtmlAttr(value) {
  return escapeHtml(value).replaceAll('`', '');
}

function normalizeKbScope(scope) {
  var v = String(scope || '').trim().toLowerCase();
  if (v === 'global') return 'Global';
  if (v === 'client') return 'Cliente';
  if (v === 'site') return 'Site';
  return scope || '-';
}

function renderInlineMarkdown(text) {
  var html = escapeHtml(text || '');
  html = html.replace(/`([^`]+)`/g, '<code>$1</code>');
  html = html.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
  html = html.replace(/\*([^*]+)\*/g, '<em>$1</em>');
  html = html.replace(/\[([^\]]+)\]\(([^)]+)\)/g, function (_m, label, rawUrl) {
    var href = String(rawUrl || '').trim();
    var safeHref = '#';
    if (/^https?:\/\//i.test(href) || /^mailto:/i.test(href)) {
      safeHref = href;
    }
    return '<a href="' + escapeHtmlAttr(safeHref) + '" target="_blank" rel="noopener noreferrer">' + label + '</a>';
  });
  return html;
}

function splitMarkdownTableRow(line) {
  var raw = String(line || '').trim();
  if (raw.startsWith('|')) raw = raw.slice(1);
  if (raw.endsWith('|')) raw = raw.slice(0, -1);
  return raw.split('|').map(function (c) { return c.trim(); });
}

function isMarkdownTableSeparator(line) {
  var raw = String(line || '').trim();
  if (!raw.includes('|')) return false;
  if (raw.startsWith('|')) raw = raw.slice(1);
  if (raw.endsWith('|')) raw = raw.slice(0, -1);
  var cells = raw.split('|').map(function (c) { return c.trim(); });
  if (!cells.length) return false;
  for (var i = 0; i < cells.length; i++) {
    if (!/^:?-{3,}:?$/.test(cells[i])) return false;
  }
  return true;
}

function getTableAlignments(separatorLine) {
  var raw = String(separatorLine || '').trim();
  if (raw.startsWith('|')) raw = raw.slice(1);
  if (raw.endsWith('|')) raw = raw.slice(0, -1);
  var cells = raw.split('|').map(function (c) { return c.trim(); });
  return cells.map(function (c) {
    var left = c.startsWith(':');
    var right = c.endsWith(':');
    if (left && right) return 'center';
    if (right) return 'right';
    if (left) return 'left';
    return '';
  });
}

function escapeRegExp(text) {
  return String(text).replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

function applySyntaxToken(html, regex, klass) {
  return html.replace(regex, function (m) {
    return '<span class="' + klass + '">' + m + '</span>';
  });
}

function highlightCodeBasic(code, lang) {
  var normalizedLang = String(lang || '').trim().toLowerCase();
  var html = escapeHtml(code || '');

  html = applySyntaxToken(html, /("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*')/g, 'kb-syntax-str');
  html = applySyntaxToken(html, /\b\d+(?:\.\d+)?\b/g, 'kb-syntax-num');

  if (normalizedLang === 'json') {
    html = applySyntaxToken(html, /"([^"\\]|\\.)*"(?=\s*:)/g, 'kb-syntax-key');
    html = applySyntaxToken(html, /\b(true|false|null)\b/g, 'kb-syntax-kw');
    return html;
  }

  if (normalizedLang === 'bash' || normalizedLang === 'sh' || normalizedLang === 'powershell' || normalizedLang === 'ps1') {
    html = applySyntaxToken(html, /#[^\n]*/g, 'kb-syntax-com');
    html = applySyntaxToken(html, /\b(if|then|else|fi|for|do|done|while|case|esac|function|return|exit)\b/g, 'kb-syntax-kw');
    return html;
  }

  html = applySyntaxToken(html, /\/\/[^\n]*/g, 'kb-syntax-com');
  html = applySyntaxToken(html, /\b(func|function|return|if|else|switch|case|default|for|range|break|continue|const|let|var|type|struct|interface|map|chan|package|import|try|catch|throw|new|class|public|private|protected|static|async|await|nil|true|false)\b/g, 'kb-syntax-kw');

  if (normalizedLang === 'go') {
    var goTypes = ['string', 'int', 'int64', 'float64', 'bool', 'byte', 'rune', 'error', 'any'];
    var goPattern = new RegExp('\\b(' + goTypes.map(escapeRegExp).join('|') + ')\\b', 'g');
    html = applySyntaxToken(html, goPattern, 'kb-syntax-typ');
  }

  return html;
}

function renderMarkdown(markdown) {
  var lines = String(markdown || '').replace(/\r\n/g, '\n').split('\n');
  var html = [];
  var inCode = false;
  var inCodeLang = '';
  var codeLines = [];
  var paragraph = [];
  var listType = '';

  function flushParagraph() {
    if (!paragraph.length) return;
    html.push('<p>' + renderInlineMarkdown(paragraph.join(' ')) + '</p>');
    paragraph = [];
  }

  function closeList() {
    if (!listType) return;
    html.push('</' + listType + '>');
    listType = '';
  }

  function renderCodeBlock(lines, fenceLang) {
    var lang = String(fenceLang || '').trim();
    var className = lang ? ' class="language-' + escapeHtmlAttr(lang.toLowerCase()) + '"' : '';
    var highlighted = highlightCodeBasic(lines.join('\n'), lang);
    return '<pre><code' + className + '>' + highlighted + '</code></pre>';
  }

  for (var i = 0; i < lines.length; i++) {
    var line = lines[i];
    var trimmed = line.trim();

    if (trimmed.startsWith('```')) {
      flushParagraph();
      closeList();
      if (!inCode) {
        inCode = true;
        var fenceInfo = trimmed.slice(3).trim();
        inCodeLang = fenceInfo ? fenceInfo.split(/\s+/)[0] : '';
        codeLines = [];
      } else {
        html.push(renderCodeBlock(codeLines, inCodeLang));
        inCode = false;
        inCodeLang = '';
      }
      continue;
    }

    if (inCode) {
      codeLines.push(line);
      continue;
    }

    if (!trimmed) {
      flushParagraph();
      closeList();
      continue;
    }

    var heading = trimmed.match(/^(#{1,6})\s+(.+)$/);
    if (heading) {
      flushParagraph();
      closeList();
      var level = heading[1].length;
      html.push('<h' + level + '>' + renderInlineMarkdown(heading[2]) + '</h' + level + '>');
      continue;
    }

    if (trimmed.includes('|') && i + 1 < lines.length && isMarkdownTableSeparator(lines[i + 1])) {
      flushParagraph();
      closeList();

      var headers = splitMarkdownTableRow(trimmed);
      var alignments = getTableAlignments(lines[i + 1]);
      html.push('<div class="kb-table-wrap"><table class="kb-markdown-table"><thead><tr>');
      for (var h = 0; h < headers.length; h++) {
        var hAlign = alignments[h] ? ' style="text-align:' + alignments[h] + '"' : '';
        html.push('<th' + hAlign + '>' + renderInlineMarkdown(headers[h]) + '</th>');
      }
      html.push('</tr></thead><tbody>');

      i += 2;
      for (; i < lines.length; i++) {
        var rowTrimmed = String(lines[i] || '').trim();
        if (!rowTrimmed || !rowTrimmed.includes('|')) {
          i -= 1;
          break;
        }
        var rowCells = splitMarkdownTableRow(rowTrimmed);
        html.push('<tr>');
        for (var c = 0; c < headers.length; c++) {
          var cell = c < rowCells.length ? rowCells[c] : '';
          var align = alignments[c] ? ' style="text-align:' + alignments[c] + '"' : '';
          html.push('<td' + align + '>' + renderInlineMarkdown(cell) + '</td>');
        }
        html.push('</tr>');
      }
      html.push('</tbody></table></div>');
      continue;
    }

    var unordered = trimmed.match(/^[-*]\s+(.+)$/);
    if (unordered) {
      flushParagraph();
      if (listType !== 'ul') {
        closeList();
        listType = 'ul';
        html.push('<ul>');
      }
      html.push('<li>' + renderInlineMarkdown(unordered[1]) + '</li>');
      continue;
    }

    var ordered = trimmed.match(/^\d+\.\s+(.+)$/);
    if (ordered) {
      flushParagraph();
      if (listType !== 'ol') {
        closeList();
        listType = 'ol';
        html.push('<ol>');
      }
      html.push('<li>' + renderInlineMarkdown(ordered[1]) + '</li>');
      continue;
    }

    closeList();
    paragraph.push(trimmed);
  }

  if (inCode) {
    html.push(renderCodeBlock(codeLines, inCodeLang));
  }
  flushParagraph();
  closeList();

  return html.join('');
}

function buildKnowledgeMeta(article) {
  return [
    '<span>' + escapeHtml(article.id || '-') + '</span>',
    '<span>' + escapeHtml(article.category || '-') + '</span>',
    '<span>Escopo: ' + escapeHtml(normalizeKbScope(article.scope)) + '</span>',
    '<span>Autor: ' + escapeHtml(article.author || '-') + '</span>',
    '<span>Nivel: ' + escapeHtml(article.difficulty || '-') + '</span>',
    '<span>Leitura: ' + escapeHtml(String(article.readTimeMin || '-')) + ' min</span>',
    '<span>Publicado: ' + escapeHtml(article.publishedAt || '-') + '</span>',
    '<span>Atualizado: ' + escapeHtml(article.updatedAt || '-') + '</span>'
  ].join('');
}

function getDiskUsagePercent(disk) {
  if (!disk.freeKnown) return null;
  var size = Number(disk.sizeGB || 0);
  var free = Number(disk.freeGB || 0);
  if (!Number.isFinite(size) || size <= 0) return 0;
  var used = Math.max(0, size - free);
  var pct = Math.round((used / size) * 100);
  return Math.min(100, Math.max(0, pct));
}

function renderDiskUsageBar(disk) {
  var usage = getDiskUsagePercent(disk);
  if (usage === null) return '';
  return '<div class="disk-bar"><span style="width: ' + escapeHtmlAttr(String(usage)) + '%"></span></div>';
}

function renderDiskUsageLabel(disk) {
  var usage = getDiskUsagePercent(disk);
  if (usage === null) return 'Uso: indisponivel';
  var freePct = 100 - usage;
  return 'Uso: ' + usage + '% | Livre: ' + freePct + '%';
}

function renderDiskOccupiedGB(disk) {
  if (!disk.freeKnown) return 'indisponivel';
  var size = Number(disk.sizeGB || 0);
  var free = Number(disk.freeGB || 0);
  if (!Number.isFinite(size) || !Number.isFinite(free) || size <= 0) return 'indisponivel';
  var occupied = Math.max(0, size - free);
  return occupied.toFixed(2) + ' GB';
}
