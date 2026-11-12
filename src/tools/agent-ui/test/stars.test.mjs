// stars.test.mjs — Regressao das estrelas de avaliacao do agent (CSAT).
//
// O BUG (reportado pelo usuario, com print): ao avaliar um chamado aparecia o
// toast "Selecione uma nota de 1 a 5" e NENHUMA estrela era visivel. Causa raiz:
// em partials/views/supportView.html cada <path> tinha o proprio <path> aninhado
// DENTRO do atributo d:
//
//   <path d="<path d="m354-287 ... Z" fill="currentColor" />" fill="currentColor" />
//
// O parser HTML le o primeiro d como o texto literal '<path d=' e o resto vira
// atributo-lixo => nenhum path valido => estrelas invisiveis => valor 0 => toast.
//
// Duas grades de estrelas usam o MESMO par de glifos (outline + fill):
//   #ratingStars       (painel de avaliacao do chamado)
//   #closeTicketStars  (modal de fechamento)
//
// COBERTURA ALEGADA (honestidade sobre o que este arquivo mede):
//   * itens 1-5 e 7: leitura dos ARTEFATOS (HTML e CSS) que rodam no agent;
//   * item 6: comportamento REAL de attachStarsWidget, extraida do IIFE e
//     executada contra um stub de DOM. O stub e NOSSO, nao um browser: cobre a
//     LOGICA (classe active, value, toggle, cap), NAO o layout/render do SVG.
//     O que troca outline por fill no clique e CSS, coberto no item 5 por leitura.
//
// Sem jsdom: nao existe jsdom no checkout (nem node_modules no frontend do
// agent) e nao instalamos dependencia - o mesmo padrao de tools/a2ui/test.

import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, existsSync, readdirSync, mkdtempSync, writeFileSync, rmSync } from "node:fs";
import { join, relative, sep } from "node:path";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";

// ─── Caminhos (test/ → tools/agent-ui/ → tools/ → src/ → Discovery/) ───
const HERE = fileURLToPath(new URL(".", import.meta.url));
const AGENT_UI_DIR = join(HERE, "..");
const SRC_DIR = join(AGENT_UI_DIR, "..", "..");
const DISCOVERY_DIR = join(SRC_DIR, "..");
const FRONTEND_DIR = join(SRC_DIR, "frontend");

const SUPPORT_VIEW = join(FRONTEND_DIR, "partials", "views", "supportView.html");
const APP_SUPPORT_JS = join(FRONTEND_DIR, "js", "app-support.js");
const APP_UTILS_JS = join(FRONTEND_DIR, "js", "app-utils.js");
const RESPONSIVE_CSS = join(FRONTEND_DIR, "styles", "responsive-part-a.css");

const read = (p) => readFileSync(p, "utf8");
const rel = (p) => relative(DISCOVERY_DIR, p).split(sep).join("/");

function frontendFiles(extensions = /\.[a-z]+$/i) {
  const out = [];
  const walk = (dir) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = join(dir, entry.name);
      if (entry.isDirectory()) {
        if (entry.name !== "node_modules") walk(full);
      } else if (extensions.test(entry.name)) {
        out.push(full);
      }
    }
  };
  walk(FRONTEND_DIR);
  return out;
}

// ═══════════════════════════════════════════════════════════════════════
// ITEM 1 — REGRESSAO EXATA (o defeito que quebrou a avaliacao)
// ═══════════════════════════════════════════════════════════════════════

test("(1) nenhum arquivo do frontend tem o markup corrompido das estrelas (d com <path aninhado)", () => {
  const culpados = [];
  for (const file of frontendFiles(/\.(?:html|js|css)$/i)) {
    const text = read(file);
    if (text.includes('<path d="<path d="')) culpados.push(rel(file));
  }
  assert.deepEqual(
    culpados,
    [],
    "arquivo(s) com '<path d=\"<path d=\"' — o parser le o d como texto literal e o glifo NAO desenha " +
      "(foi a causa das estrelas invisiveis):",
  );
});

test("(1) nenhum arquivo do frontend tem a cauda orfa de atributo (/>\" fill=\"currentColor\" />)", () => {
  const culpados = [];
  for (const file of frontendFiles(/\.(?:html|js|css)$/i)) {
    const text = read(file);
    if (text.includes('/>" fill="currentColor" />')) culpados.push(rel(file));
  }
  assert.deepEqual(culpados, [], "arquivo(s) com a cauda orfa do markup corrompido:");
});

// ═══════════════════════════════════════════════════════════════════════
// ITEM 2 — A CLASSE DO DEFEITO (nao so a instancia)
// ═══════════════════════════════════════════════════════════════════════
// Um valor de atributo SVG nunca pode conter '<'. Isso pega QUALQUER variacao do
// defeito (outro elemento, outro atributo), nao apenas o <path d=...> de hoje.

const SVG_ATTRS = ["d", "points", "transform", "clip-path", "mask", "fill", "stroke"];

test("(2) nenhum atributo SVG (d/points/transform/clip-path/mask/fill/stroke) tem '<' no valor", () => {
  const attrRe = new RegExp('\\b(' + SVG_ATTRS.join("|") + ')\\s*=\\s*"([^"]*)"', "g");
  const culpados = [];

  for (const file of frontendFiles(/\.(?:html|js)$/i)) {
    const text = read(file);
    let m;
    while ((m = attrRe.exec(text)) !== null) {
      const [, attr, value] = m;
      if (value.includes("<")) {
        const line = text.slice(0, m.index).split("\n").length;
        culpados.push(`${rel(file)}:${line} ${attr}="${value.slice(0, 60)}..."`);
      }
    }
  }

  assert.deepEqual(
    culpados,
    [],
    "atributo(s) SVG com '<' no valor — markup quebrado (a classe do defeito das estrelas):",
  );
});

// ═══════════════════════════════════════════════════════════════════════
// ITEM 3 — INTEGRIDADE DAS DUAS GRADES DE ESTRELAS
// ═══════════════════════════════════════════════════════════════════════
// As DUAS grades usam os mesmos glifos: #ratingStars (painel de avaliacao) e
// #closeTicketStars (modal de fechamento). Se so uma for corrigida, o usuario
// reencontra o bug na outra.

const STAR_GRIDS = ["ratingStars", "closeTicketStars"];

/** Extrai o HTML interno de <div id="..." class="stars-row"> ... </div> (nao aninhado). */
function starsRow(html, id) {
  const startRe = new RegExp(`<div[^>]*id="${id}"[^>]*>`, "i");
  const m = startRe.exec(html);
  assert.ok(m, `nao achei o container #${id} em supportView.html`);
  const from = m.index + m[0].length;
  const end = html.indexOf("</div>", from);
  assert.ok(end > from, `nao achei o fechamento de #${id}`);
  return html.slice(from, end);
}

/** Botoes .star-btn de um trecho, com data-value e paths. */
function starButtons(rowHtml) {
  const btns = [];
  const btnRe = /<button\b[^>]*class="[^"]*star-btn[^"]*"[^>]*>([\s\S]*?)<\/button>/g;
  let m;
  while ((m = btnRe.exec(rowHtml)) !== null) {
    const full = m[0];
    const value = /data-value="(\d+)"/.exec(full)?.[1];
    const paths = [...full.matchAll(/<path\b[^>]*\bd="([^"]*)"/g)].map((x) => x[1]);
    btns.push({ value: value === undefined ? null : Number(value), paths });
  }
  return btns;
}

for (const gridId of STAR_GRIDS) {
  test(`(3) #${gridId}: 5 botoes .star-btn com data-value 1..5`, () => {
    const btns = starButtons(starsRow(read(SUPPORT_VIEW), gridId));

    assert.equal(btns.length, 5, `#${gridId} deveria ter 5 botoes .star-btn`);
    assert.deepEqual(
      btns.map((b) => b.value),
      [1, 2, 3, 4, 5],
      `#${gridId}: data-value fora de 1..5 ou fora de ordem (o widget usa data-value como nota)`,
    );
  });

  test(`(3) #${gridId}: cada botao tem 2 paths (outline + fill) com d VALIDO`, () => {
    const btns = starButtons(starsRow(read(SUPPORT_VIEW), gridId));

    for (const btn of btns) {
      assert.equal(btn.paths.length, 2, `#${gridId} data-value=${btn.value}: esperado outline + fill (2 paths)`);
      for (const d of btn.paths) {
        assert.ok(d.length > 0, `#${gridId} data-value=${btn.value}: path com d vazio`);
        assert.ok(!d.includes("<"), `#${gridId} data-value=${btn.value}: d contem '<' (markup quebrado)`);
        assert.match(d, /^[Mm]/, `#${gridId} data-value=${btn.value}: d deveria comecar com M/m — medido: ${d.slice(0, 40)}`);
        assert.match(d, /[Zz]\s*$/, `#${gridId} data-value=${btn.value}: d deveria terminar com Z/z — medido: ...${d.slice(-40)}`);
      }
    }
  });

  test(`(4) #${gridId}: os 5 botoes usam os MESMOS 2 paths (nenhuma estrela com desenho diferente)`, () => {
    const btns = starButtons(starsRow(read(SUPPORT_VIEW), gridId));
    const assinatura = (b) => JSON.stringify(b.paths);
    const distintas = [...new Set(btns.map(assinatura))];

    assert.equal(
      distintas.length,
      1,
      `#${gridId}: os botoes deveriam compartilhar os mesmos 2 paths; achei ${distintas.length} desenhos diferentes`,
    );
  });
}

// ═══════════════════════════════════════════════════════════════════════
// ITEM 5 — CSS QUE FAZ O WIDGET FUNCIONAR
// ═══════════════════════════════════════════════════════════════════════
// As tres ultimas regras sao as que fazem o clique MUDAR ALGO VISIVEL: o CSS
// troca qual dos dois SVGs aparece. Sem elas, clicar muda a classe mas o usuario
// continua vendo a mesma coisa.

test("(5) as regras CSS do widget de estrelas existem (inclusive a troca outline/fill)", () => {
  const css = read(RESPONSIVE_CSS);

  const regras = [
    [/\.stars-row\s*\{/, ".stars-row"],
    [/\.star-btn\s*\{/, ".star-btn"],
    [/\.star-btn\.active\s*\{/, ".star-btn.active"],
    [/\.star-icon--fill\s*\{\s*display:\s*none/, ".star-icon--fill { display: none }"],
    [/\.star-btn\.active\s+\.star-icon--outline\s*\{\s*display:\s*none/, ".star-btn.active .star-icon--outline { display: none }"],
    [/\.star-btn\.active\s+\.star-icon--fill\s*\{\s*display:\s*block/, ".star-btn.active .star-icon--fill { display: block }"],
  ];

  const ausentes = regras.filter(([re]) => !re.test(css)).map(([, nome]) => nome);
  assert.deepEqual(ausentes, [], "regra(s) CSS ausente(s) em styles/responsive-part-a.css:");
});

// ═══════════════════════════════════════════════════════════════════════
// ITEM 6 — O WIDGET, COM COBERTURA DE RUNTIME REAL
// ═══════════════════════════════════════════════════════════════════════
// attachStarsWidget vive DENTRO de um IIFE em js/app-support.js e nao tem
// export. Em vez de "testar por leitura do fonte" (que nao mede nada), extraimos
// a FUNCAO por contagem de chaves, carregamos num modulo temporario e EXECUTAMOS
// contra um stub minimo de DOM.
//
// LIMITE DECLARADO: o stub e nosso, nao um browser. Ele cobre a LOGICA do widget
// (value, classe active, aria-pressed, toggle, cap). NAO cobre layout nem o
// render do SVG - isso e CSS (item 5) e o parse do HTML (itens 1-4).

const WIDGET_FN = "attachStarsWidget";

/** Extrai o fonte de uma funcao de topo por contagem de chaves. */
function extractFunction(source, name) {
  const marker = "function " + name + "(";
  const start = source.indexOf(marker);
  assert.ok(start >= 0, `nao achei "function ${name}(" em app-support.js`);
  let i = source.indexOf("{", start);
  assert.ok(i > start, `nao achei o corpo de ${name}`);
  let depth = 0;
  for (; i < source.length; i++) {
    if (source[i] === "{") depth++;
    else if (source[i] === "}") {
      depth--;
      if (depth === 0) return source.slice(start, i + 1);
    }
  }
  assert.fail(`nao consegui delimitar o corpo de ${name} (chaves desbalanceadas)`);
}

/** Stub minimo: apenas o que attachStarsWidget usa. */
function stubElement(attrs = {}) {
  const _attrs = { ...attrs };
  const _classes = new Set();
  return {
    _attrs,
    _classes,
    _click: null,
    getAttribute: (n) => (n in _attrs ? _attrs[n] : null),
    setAttribute: (n, v) => { _attrs[n] = String(v); },
    classList: {
      toggle: (c, on) => { if (on) _classes.add(c); else _classes.delete(c); },
      contains: (c) => _classes.has(c),
    },
    addEventListener(ev, fn) { if (ev === "click") this._click = fn; },
  };
}

function stubContainer(n = 5) {
  const btns = [];
  for (let v = 1; v <= n; v++) btns.push(stubElement({ "data-value": String(v) }));
  return { _btns: btns, querySelectorAll: (sel) => (sel === ".star-btn" ? btns : []) };
}

/** Carrega attachStarsWidget de verdade a partir do fonte do agent. */
async function loadStarsWidget() {
  const source = read(APP_SUPPORT_JS);
  const body = extractFunction(source, WIDGET_FN);
  const dir = mkdtempSync(join(tmpdir(), "agent-ui-stars-"));
  const file = join(dir, "widget.mjs");
  writeFileSync(file, `${body}\nexport { ${WIDGET_FN} };\n`, "utf8");
  try {
    const mod = await import(urlHref(file));
    assert.equal(typeof mod[WIDGET_FN], "function", "a extracao nao devolveu uma funcao");
    return mod[WIDGET_FN];
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

const urlHref = (p) => new URL(`file://${p.replace(/\\/g, "/")}`).href;

test("(6) widget: 0/NaN/negativo -> 0 (nada selecionado)", async () => {
  const attach = await loadStarsWidget();

  for (const entrada of [0, NaN, -1, null, undefined, "", "abc"]) {
    const w = attach(stubContainer(), stubElement());
    w.set(entrada);
    assert.equal(w.value, 0, `set(${String(entrada)}) deveria resultar em 0`);
  }
});

test("(6) widget: 1..5 e preservado", async () => {
  const attach = await loadStarsWidget();

  for (const entrada of [1, 2, 3, 4, 5, "3"]) {
    const w = attach(stubContainer(), stubElement());
    w.set(entrada);
    assert.equal(w.value, Number(entrada), `set(${String(entrada)}) deveria preservar o valor`);
  }
});

test("(6) widget: >5 e capado em 5", async () => {
  const attach = await loadStarsWidget();

  for (const entrada of [6, 99, 1000, "42"]) {
    const w = attach(stubContainer(), stubElement());
    w.set(entrada);
    assert.equal(w.value, 5, `set(${String(entrada)}) deveria capar em 5`);
  }
});

test("(6) widget: clicar no MESMO valor desmarca; em outro valor, seleciona", async () => {
  const attach = await loadStarsWidget();
  const container = stubContainer(5);
  const w = attach(container, stubElement());

  container._btns[2]._click();               // clique no valor 3
  assert.equal(w.value, 3, "clicar no 3 deveria selecionar 3");

  container._btns[2]._click();               // mesmo valor -> desmarca
  assert.equal(w.value, 0, "clicar no MESMO valor deveria desmarcar (voltar a 0)");

  container._btns[4]._click();               // valor 5
  assert.equal(w.value, 5, "clicar no 5 deveria selecionar 5");
});

test("(6) widget: pinta a classe active ate o valor escolhido e nada alem", async () => {
  const attach = await loadStarsWidget();
  const container = stubContainer(5);
  const w = attach(container, stubElement());

  w.set(3);
  assert.deepEqual(
    container._btns.map((b) => (b._classes.has("active") ? 1 : 0)),
    [1, 1, 1, 0, 0],
    "estrelas 1..3 deveriam ficar ativas e 4..5 nao",
  );

  w.set(0);
  assert.deepEqual(
    container._btns.map((b) => (b._classes.has("active") ? 1 : 0)),
    [0, 0, 0, 0, 0],
    "com valor 0 nenhuma estrela deveria estar ativa",
  );
});

test("(6) widget: o botao de limpar so aparece quando ha nota (valor 0 -> oculto)", async () => {
  const attach = await loadStarsWidget();
  const clear = stubElement();
  clear._classes.add("hidden");
  const w = attach(stubContainer(5), clear);

  assert.ok(clear._classes.has("hidden"), "com valor inicial 0 o botao de limpar deveria estar oculto");

  w.set(4);
  assert.ok(!clear._classes.has("hidden"), "com nota escolhida o botao de limpar deveria aparecer");

  w.reset();
  assert.ok(clear._classes.has("hidden"), "apos reset o botao de limpar deveria voltar a ficar oculto");
});

test("(6) widget: onChange recebe o valor efetivo (inclusive o 0 da limpeza)", async () => {
  const attach = await loadStarsWidget();
  const w = attach(stubContainer(5), stubElement());
  const vistos = [];
  w.onChange = (v) => vistos.push(v);

  w.set(2);
  w.set(0);

  assert.deepEqual(vistos, [2, 0], "onChange deveria receber 2 e depois 0");
});

// ═══════════════════════════════════════════════════════════════════════
// ITEM 7 — O TOAST: valor 0 E O COMPORTAMENTO CORRETO, NAO O BUG
// ═══════════════════════════════════════════════════════════════════════
// O usuario viu "Selecione uma nota de 1 a 5". Esse caminho (enviar sem nota)
// esta CERTO e continua existindo de proposito. O bug era NAO CONSEGUIR escolher
// a nota (markup quebrado) — nao o aviso. Este teste deixa isso explicito para
// ninguem "consertar" removendo a validacao.

test("(7) o texto do aviso existe em pt e en (o caminho de valor 0 e intencional)", () => {
  const utils = read(APP_UTILS_JS);

  assert.match(utils, /"support\.selectRating"\s*:\s*"Selecione uma nota de 1 a 5"/,
    "falta o texto pt de support.selectRating (o aviso que o usuario viu)");
  assert.match(utils, /"support\.selectRating"\s*:\s*"Select a rating from 1 to 5"/,
    "falta o texto en de support.selectRating");
});

test("(7) o envio sem nota mostra o aviso e NAO prossegue (validacao preservada)", () => {
  const support = read(APP_SUPPORT_JS);

  // O disparo do aviso no caminho de valor 0 precisa continuar existindo.
  assert.match(support, /showToast\(\s*translate\(\s*['"]support\.selectRating['"]\s*\)/,
    "o aviso de support.selectRating sumiu do app-support.js — o caminho de valor 0 deveria continuar avisando");

  // E precisa ser um erro (nao um toast informativo).
  const idx = support.search(/showToast\(\s*translate\(\s*['"]support\.selectRating['"]\s*\)/);
  const trecho = support.slice(idx, idx + 200);
  assert.match(trecho, /['"]error['"]/, `o aviso deveria ser do tipo erro — medido: ${trecho.slice(0, 120)}`);
});
