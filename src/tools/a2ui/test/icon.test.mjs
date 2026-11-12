// icon.test.mjs — Testes do componente Icon do A2UI (achado R7).
//
// CONTEXTO (a direção mudou durante o trabalho — leia antes de julgar os testes):
// A primeira hipótese era desenhar o glifo por SVG inline e REMOVER a webfont.
// A decisão final do usuário foi a oposta: a webfont Material Symbols é
// EMPACOTADA LOCALMENTE no agent (servida pela origem do próprio app) e o
// componente Icon do A2UI volta a ser o VENDORADO, emitindo
// <span class="material-symbols-outlined a2ui-icon">NAME</span>.
//
// Portanto:
//   * "material-symbols-outlined" no bundle NÃO é bug — é o comportamento
//     correto (o componente vendorado resolve o glifo por LIGADURA).
//   * O R7 exige que essa ligadura RESOLVA sem rede externa: fonte presente e
//     alcançável localmente, @font-face relativo, as propriedades de ligadura e
//     de eixo variável, e a regra chegando ao SHADOW DOM (onde o componente
//     realmente renderiza).
//
// POR QUE RODA SEM BROWSER:
//   jsdom NÃO existe em node_modules, e @lit-labs/ssr-dom-shim não instala os
//   globais (falta adoptedStyleSheets em Document.prototype), então montar o
//   custom element aqui é inviável. A prova é feita sobre os ARTEFATOS que
//   rodam no agente (CSS servido, injeção no shadow root, bundle compilado e o
//   .woff2 real) — não sobre uma reimplementação do componente.

import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, existsSync, readdirSync, statSync } from "node:fs";
import { join, relative, sep } from "node:path";
import { fileURLToPath } from "node:url";

// ─── Caminhos (test/ → tools/a2ui/ → tools/ → src/ → Discovery/) ───
const HERE = fileURLToPath(new URL(".", import.meta.url));
const A2UI_DIR = join(HERE, "..");
const SRC_DIR = join(A2UI_DIR, "..", "..");
const DISCOVERY_DIR = join(SRC_DIR, "..");
const FRONTEND_DIR = join(SRC_DIR, "frontend");
const REPO_DIR = join(DISCOVERY_DIR, "..");

const API_VALIDATOR = join(
  REPO_DIR, "DiscoveryRMM_API", "src", "Discovery.Infrastructure", "Services", "AiChatA2uiValidator.cs",
);
const VENDOR_BASIC_COMPONENTS = join(
  A2UI_DIR, "node_modules", "@a2ui", "web_core", "src", "v0_9", "basic_catalog", "components", "basic_components.js",
);
const ICON_FONT_CSS = join(FRONTEND_DIR, "styles", "icon-font.css");
const ICON_NAMES_JS = join(FRONTEND_DIR, "js", "a2ui-icon-names.js");
const APP_CHAT_JS = join(FRONTEND_DIR, "js", "app-chat.js");
const BUNDLE_JS = join(FRONTEND_DIR, "a2ui-bundle.js");
const GLYPH_FIXTURE = join(HERE, "fixtures", "font-glyphs.json");

const read = (p) => readFileSync(p, "utf8");
const rel = (p) => relative(DISCOVERY_DIR, p).split(sep).join("/");
const toSnake = (camel) => camel.replace(/([a-z0-9])([A-Z])/g, "$1_$2").toLowerCase();

// ─── Extração das listas de nomes ───
// A fonte da verdade é o catálogo VENDORADO (ICON_NAMES); as outras listas
// precisam andar junto com ele.

/** ICON_NAMES de @a2ui/web_core (catálogo vendorado). */
function vendoredIconNames() {
  const block = read(VENDOR_BASIC_COMPONENTS).match(/const ICON_NAMES = \[([\s\S]*?)\];/);
  assert.ok(block, "não achei 'const ICON_NAMES = [' no catálogo vendorado");
  return [...block[1].matchAll(/'([A-Za-z0-9]+)'/g)].map((m) => m[1]);
}

/** IconNamesList do validador C#. */
function validatorIconNames() {
  const block = read(API_VALIDATOR).match(/IconNamesList\s*=\s*\{([\s\S]*?)\};/);
  assert.ok(block, "não achei 'IconNamesList = {' em AiChatA2uiValidator.cs");
  return [...block[1].matchAll(/"([A-Za-z0-9]+)"/g)].map((m) => m[1]);
}

/** window.A2uiIconNames de frontend/js/a2ui-icon-names.js (script clássico/IIFE). */
function iconNamesApi() {
  const src = read(ICON_NAMES_JS);
  const prev = globalThis.window;
  globalThis.window = globalThis.window ?? {};
  try {
    new Function(src)();
    const api = globalThis.window.A2uiIconNames;
    assert.ok(api, "a2ui-icon-names.js não publicou window.A2uiIconNames");
    return api;
  } finally {
    if (prev === undefined) delete globalThis.window;
  }
}


// ─── Detecção de referência externa ───
// Procuramos REFERÊNCIAS ATIVAS (href=, src=, url(), @import), e NÃO a substring
// "fonts.googleapis.com" no texto. Motivo medido: três arquivos do agent CITAM o
// domínio em COMENTÁRIO que documenta a proibição (icon-font.css:17, index.html,
// p2p-debug.html). Uma busca por substring daria falsos positivos e alguém
// "consertaria" o teste apagando a documentação. A regra por referência ativa é a
// que pega o caso real (um <link href="https://fonts.googleapis.com/..."> de
// verdade, como o que existia em p2p-debug.html antes da correção).
const EXTERNAL_HOST = /^(?:https?:)?\/\//i;

function activeExternalRefs(text) {
  const out = [];
  const re = /(?:href|src)\s*=\s*["']([^"']+)["']|url\(\s*["']?([^"')]+)["']?\s*\)|@import\s+(?:url\(\s*)?["']([^"')]+)["']/gi;
  let m;
  while ((m = re.exec(text)) !== null) {
    const value = m[1] ?? m[2] ?? m[3];
    if (value && EXTERNAL_HOST.test(value.trim())) out.push(value.trim());
  }
  return out;
}

function frontendSourceFiles() {
  const out = [];
  const walk = (dir) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = join(dir, entry.name);
      if (entry.isDirectory()) {
        if (entry.name !== "node_modules") walk(full);
      } else if (/\.(?:js|mjs|css|html)$/i.test(entry.name)) {
        out.push(full);
      }
    }
  };
  walk(FRONTEND_DIR);
  return out;
}

// ═══════════════════════════════════════════════════════════════════════
// PARTE 1 — CONTRATOS QUE DEVEM PASSAR (estado atual verificado)
// ═══════════════════════════════════════════════════════════════════════

// ─── (a) PARIDADE DAS LISTAS ───
// Impede o catálogo do servidor divergir do que o renderer desenha.

test("(a) paridade: ICON_NAMES (vendorado) == IconNames (validador C#) — mesmo conteúdo e ordem", () => {
  const js = vendoredIconNames();
  const cs = validatorIconNames();

  assert.ok(js.length > 0, "ICON_NAMES vendorado veio vazio");
  assert.deepEqual(
    cs,
    js,
    "AiChatA2uiValidator.IconNamesList divergiu do ICON_NAMES do catálogo vendorado " +
      "(o servidor descartaria a surface de um nome que o renderer aceita, ou vice-versa)",
  );
});

test("(a) paridade: toda chave do mapa de ligaduras pertence ao catálogo", () => {
  const catalogo = new Set(vendoredIconNames());
  const { ALIASES } = iconNamesApi();

  const orfas = Object.keys(ALIASES).filter((key) => ![...catalogo].some((n) => toSnake(n) === key));
  assert.deepEqual(orfas, [], "alias(es) que não correspondem a nenhum nome do catálogo:");
});

// ─── (e)/(h) NORMALIZAÇÃO DE NOMES ───
// O componente vendorado escreve o nome cru do catálogo no <span>, mas a ligadura
// da fonte é snake_case. Sem esta normalização o ícone sai como TEXTO.

test("(e) normalização: camelCase/snake_case/kebab-case/CAIXA ALTA convergem para a ligadura snake_case", () => {
  const { a2uiIconLigature } = iconNamesApi();

  const casos = [
    ["arrowBack", "arrow_back"],
    ["arrow_back", "arrow_back"],
    ["arrow-back", "arrow_back"],
    ["ARROW_BACK", "arrow_back"],
    ["shoppingCart", "shopping_cart"],
    ["accountCircle", "account_circle"],
    ["warning", "warning"],
  ];
  for (const [input, esperado] of casos) {
    assert.equal(
      a2uiIconLigature(input),
      esperado,
      `a2uiIconLigature(${JSON.stringify(input)}) deveria devolver ${JSON.stringify(esperado)}`,
    );
  }
});

test("(e) normalização: entrada vazia, só espaços ou nula devolve string vazia", () => {
  const { a2uiIconLigature } = iconNamesApi();
  for (const valor of ["", "   ", "\t", null, undefined]) {
    assert.equal(a2uiIconLigature(valor), "", `entrada ${JSON.stringify(valor)} deveria devolver ""`);
  }
});

test("(e) normalização: todo nome do catálogo produz uma ligadura não vazia", () => {
  const { a2uiIconLigature } = iconNamesApi();
  const vazios = vendoredIconNames().filter((nome) => !a2uiIconLigature(nome));
  assert.deepEqual(vazios, [], "nomes do catálogo que a normalização não resolve:");
});

// ─── (d) ZERO DEPENDÊNCIA DE REDE EXTERNA ───

test("(d) R7: nenhuma fonte do agent é carregada por referência de rede externa (googleapis/gstatic)", () => {
  const culpados = [];
  for (const file of frontendSourceFiles()) {
    const refs = activeExternalRefs(read(file)).filter((r) => /fonts\.(?:googleapis|gstatic)\.com/i.test(r));
    if (refs.length) culpados.push(`${rel(file)} → ${refs.join(", ")}`);
  }
  assert.deepEqual(culpados, [], "arquivo(s) do agent com referência ATIVA a fonte externa (o R7 exige fonte local):");
});

test("(d) R7: o bundle não faz referência a nenhuma URL externa", () => {
  assert.ok(existsSync(BUNDLE_JS), "a2ui-bundle.js não encontrado (gere com 'npm run build')");
  assert.deepEqual(activeExternalRefs(read(BUNDLE_JS)), [], "o bundle referencia URL absoluta:");
});

// ─── (c)/(e) A FONTE É LOCAL E A LIGADURA RESOLVE ───
// O coração do R7: "fonte presente" NÃO basta. Sem as propriedades de ligadura e
// de eixo variável, a fonte existe e o glifo continua saindo como TEXTO.

test("(c) R7: @font-face aponta para .woff2 LOCAL, com caminho RELATIVO, e o arquivo existe", () => {
  const css = read(ICON_FONT_CSS);
  assert.match(css, /@font-face\s*\{/, "icon-font.css sem @font-face");

  const srcMatch = css.match(/src:\s*url\(\s*["']?([^"')]+)["']?\s*\)/);
  assert.ok(srcMatch, "não achei 'src: url(...)' no @font-face");

  const url = srcMatch[1].trim();
  assert.ok(!EXTERNAL_HOST.test(url), `o @font-face aponta para URL externa (${url}) — o R7 exige fonte local`);
  assert.ok(url.startsWith("../") || url.startsWith("./"), `o caminho da fonte deveria ser relativo (medido: ${url})`);

  const alvo = join(FRONTEND_DIR, "styles", url);
  assert.ok(existsSync(alvo), `o .woff2 referenciado pelo @font-face não existe: ${url}`);
  assert.equal(
    readFileSync(alvo).subarray(0, 4).toString("latin1"),
    "wOF2",
    "o arquivo de fonte não tem assinatura WOFF2",
  );
});

test("(c) R7: o .woff2 está sob frontend/, alcançado por '//go:embed all:frontend'", () => {
  const mainGo = read(join(SRC_DIR, "main.go"));
  assert.match(
    mainGo,
    /\/\/go:embed\s+all:frontend/,
    "o embed do frontend deixou de ser 'all:frontend' — arquivos novos (como a fonte) podem não entrar no binário",
  );

  const woff2 = [];
  const walk = (dir) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = join(dir, entry.name);
      if (entry.isDirectory()) walk(full);
      else if (entry.name.endsWith(".woff2")) woff2.push(full);
    }
  };
  walk(FRONTEND_DIR);

  assert.ok(woff2.length > 0, "nenhum .woff2 sob frontend/ — a fonte não seria embarcada");
  for (const f of woff2) assert.ok(statSync(f).size > 0, `arquivo de fonte vazio: ${rel(f)}`);
});

test("(c) R7: a regra do glifo tem font-family + 'liga' + eixos variáveis (sem isso o glifo não resolve)", () => {
  const css = read(ICON_FONT_CSS);

  assert.match(css, /\.material-symbols-outlined\s*\{[^}]*font-family/, "a classe .material-symbols-outlined não declara font-family");
  assert.match(css, /font-feature-settings:\s*["']liga["']/i, 'falta font-feature-settings:"liga" — sem isso o nome do ícone continua sendo TEXTO');
  assert.match(css, /font-variation-settings:[^;]*["']FILL["']\s*0/i, "falta font-variation-settings com FILL 0 (o contorno é o padrão da plataforma)");
  assert.match(css, /font-variation-settings:[^;]*["']wght["']\s*400/i, "falta font-variation-settings com wght 400");
});

test("(c) R7: styles.css importa o icon-font.css (senão a regra nunca chega ao documento)", () => {
  assert.match(
    read(join(FRONTEND_DIR, "styles.css")),
    /@import[^;]*icon-font\.css/,
    "styles.css não importa styles/icon-font.css — a regra do glifo não seria aplicada",
  );
});

// ─── (c') SHADOW DOM — é AQUI que o bug voltaria ───
// O Icon do A2UI é custom element Lit com shadow root. Uma regra no light DOM
// NÃO atravessa a fronteira do shadow root, e o <span> do ícone vive lá dentro.

test("(c') R7: o app injeta a regra .a2ui-icon no SHADOW ROOT (o light DOM não alcança o componente)", () => {
  // O CSS injetado vive dentro de um LITERAL DE STRING do app-chat.js, então as
  // aspas do CSS aparecem escapadas na fonte (font-feature-settings:\"liga\").
  // Normalizamos os escapes antes de casar — senão o teste falha por causa da
  // forma do arquivo, não do conteúdo (foi o que aconteceu na 1ª versão).
  const js = read(APP_CHAT_JS).replace(/\\"/g, '"');

  assert.match(js, /\.a2ui-icon\s*\{[^}]*font-family/, "app-chat.js não injeta .a2ui-icon com font-family no shadow root");
  assert.match(js, /\.a2ui-icon\s*\{[^}]*font-feature-settings:\s*["']liga["']/i, 'a regra .a2ui-icon injetada não tem font-feature-settings:"liga"');
  assert.match(js, /\.a2ui-icon\s*\{[^}]*font-variation-settings/i, "a regra .a2ui-icon injetada não tem font-variation-settings");
  assert.match(js, /src:\s*url\(\s*["']?(?:\.\.\/)?assets\/fonts\//, "o @font-face sob demanda não aponta para assets/fonts/ local");
  assert.doesNotMatch(js, /src:\s*url\(\s*["']?https?:/i, "o @font-face injetado aponta para URL externa");
});

test("(c') R7: a injeção percorre shadow roots abertos (não só o light DOM)", () => {
  assert.match(read(APP_CHAT_JS), /shadowRoot/, "app-chat.js não menciona shadowRoot — o tema/fonte não alcançaria o Icon");
});

test("(c') R7: a fonte é injetada SOB DEMANDA, marcada e idempotente", () => {
  const js = read(APP_CHAT_JS);
  assert.match(js, /["']a2ui-icon["']/, "app-chat.js não procura <a2ui-icon> para decidir se injeta a fonte");
  assert.match(js, /data-a2ui-symbols/, "falta a marca data-a2ui-symbols (identificação/idempotência da injeção)");
});

// ─── (b) O COMPONENTE VENDORADO ESTÁ NO BUNDLE ───
// Com a webfont local, o <span> do vendorado é o comportamento CORRETO.

test("(b) bundle: traz o componente Icon do catálogo emitindo o <span> de ligadura (correto no R7 atual)", () => {
  const bundle = read(BUNDLE_JS);
  assert.match(bundle, /material-symbols-outlined/, "o bundle não contém 'material-symbols-outlined' (o Icon vendorado sumiu)");
  assert.match(bundle, /a2ui-icon/, "o bundle não contém a classe 'a2ui-icon'");
});

test("(b) bundle: a normalização de nomes está ligada ao app (nome cru viraria texto)", () => {
  assert.ok(existsSync(ICON_NAMES_JS), "frontend/js/a2ui-icon-names.js não existe");
  assert.match(read(join(FRONTEND_DIR, "index.html")), /a2ui-icon-names\.js/, "index.html não carrega js/a2ui-icon-names.js — a normalização nunca rodaria");
  assert.match(read(APP_CHAT_JS), /a2uiNormalizeIconNames|A2uiIconNames/, "app-chat.js não usa a normalização de nomes do Icon");
});

// ═══════════════════════════════════════════════════════════════════════
// PARTE 2 — LIGADURA × FONTE: três medições, uma armadilha
//
// LEIA ANTES DE MEXER — este bloco existe porque o mesmo "defeito" foi
// "encontrado" três vezes com instrumentos diferentes, e só a última medição
// estava certa. Quem usar o instrumento errado VAI "achar" defeitos inexistentes
// e — pior — pode ser tentado a mexer no produto para satisfazer o teste.
//
//   1) getGlyphOrder()          → "existe um GLIFO com o nome X?"
//       6683 nomes INTERNOS de glifo. NÃO é o critério de ligadura: dá FALSO
//       NEGATIVO. Ex.: location_on/payment/phone não aparecem aqui, mas o texto
//       resolve normalmente. Foi a primeira medição feita neste trabalho e ela
//       sugeriu 7 nomes quebrados — 3 deles inexistentes.
//   2) shaper (HarfBuzz)        → "o TEXTO X vira UM glifo?"
//       Mede o resultado final, mas não separa ligadura de desenho comum.
//   3) GSUB direto (verificador)→ "existe uma LIGADURA com esse nome?"
//       A FONTE DA VERDADE. Medido: 4319 ligaduras; o cmap tem 4425 entradas.
//       É uma grandeza DIFERENTE de getGlyphOrder (6683) — não são comparáveis.
//
// VEREDITO (medido pelo shaper e confirmado no GSUB): os 59 nomes do catálogo
// RESOLVEM. Os 4 que precisam de alias (favorite_off, star_off, play, rewind)
// estão cobertos pelo mapa em frontend/js/a2ui-icon-names.js. Este item deve
// ficar VERDE: um vermelho aqui seria FALSO e induziria alguém a "consertar" um
// produto que está correto.
//
// O teste da armadilha fica ao lado do teste correto de propósito: é o registro
// de que medir com o instrumento errado inventa defeito.
// ═══════════════════════════════════════════════════════════════════════
/** Medição ingênua: nomes de glifo existentes na fonte (NÃO é o critério de ligadura). */
function fontGlyphs() {
  assert.ok(
    existsSync(GLYPH_FIXTURE),
    `faltando ${rel(GLYPH_FIXTURE)} — rode: python test/generate-font-glyphs.py (precisa de fonttools+brotli+uharfbuzz)`,
  );
  return new Set(JSON.parse(read(GLYPH_FIXTURE)).glyphs);
}

/** Medição correta: o que o SHAPER produz para cada texto (nome → lista de glifos). */
function shapedGlyphs() {
  assert.ok(existsSync(GLYPH_FIXTURE), `faltando ${rel(GLYPH_FIXTURE)} — rode generate-font-glyphs.py`);
  const data = JSON.parse(read(GLYPH_FIXTURE));
  assert.ok(
    data.shaped && Object.keys(data.shaped).length > 0,
    "a fixture não tem a medição do shaper — regenere com generate-font-glyphs.py (uharfbuzz instalado)",
  );
  return data.shaped;
}

/** Um texto "resolve" quando o shaper devolve UM único glifo. */
const resolvesToSingleGlyph = (shaped, text) => Array.isArray(shaped[text]) && shaped[text].length === 1;

// ─── O TESTE CORRETO (critério do shaper) ───

test("(f) R7/shaper: todo nome do catálogo resolve para UM glifo (direto ou via alias)", () => {
  const shaped = shapedGlyphs();
  const { ALIASES, a2uiIconLigature } = iconNamesApi();

  const semGlifo = vendoredIconNames()
    .map((nome) => {
      const ligadura = a2uiIconLigature(nome);
      // O app normaliza e depois aplica o alias (ver a2uiIconLigature).
      const final = ligadura || "";
      return { nome, ligadura: final };
    })
    .filter(({ ligadura }) => !resolvesToSingleGlyph(shaped, ligadura))
    .map(({ nome, ligadura }) => {
      const glifos = shaped[ligadura];
      const detalhe = Array.isArray(glifos) ? glifos.join("+") : "(não medido)";
      return `${nome} → ${ligadura} = ${detalhe}`;
    });

  assert.deepEqual(
    semGlifo,
    [],
    "nome(s) do catálogo cujo texto NÃO vira um glifo no shaper — estes ícones " +
      "saem como TEXTO no chat (o sintoma do R7):",
  );
});

test("(f) R7/shaper: todo ALIAS aponta para um texto que resolve para UM glifo", () => {
  const shaped = shapedGlyphs();
  const { ALIASES } = iconNamesApi();

  const quebrados = Object.entries(ALIASES)
    .filter(([, destino]) => !resolvesToSingleGlyph(shaped, destino))
    .map(([origem, destino]) => `${origem} → ${destino} = ${(shaped[destino] || []).join("+") || "(não medido)"}`);

  assert.deepEqual(quebrados, [], "alias(es) apontando para texto que não vira um glifo:");
});

// ─── O TESTE DA ARMADILHA (critério ingênuo, mantido para documentar) ───

test("(f) armadilha documentada: getGlyphOrder() NÃO é critério de ligadura (mede o instrumento errado)", () => {
  const glifos = fontGlyphs();
  const { a2uiIconLigature } = iconNamesApi();

  // Nomes que NÃO têm glifo com o próprio nome, mas que o shaper RESOLVE por
  // ligadura. Se este teste quebrar, alguém "consertou" a fonte ou trocou o
  // mecanismo — revise os DOIS testes antes de mexer.
  const resolvidosPorLigadura = ["location_on", "payment", "phone"];

  const semGlifoProprio = resolvidosPorLigadura.filter((n) => !glifos.has(n));
  assert.deepEqual(
    semGlifoProprio.sort(),
    [...resolvidosPorLigadura].sort(),
    "estes nomes passaram a ter glifo próprio: a armadilha documentada mudou — " +
      "revise os comentários da PARTE 2 e o gerador da fixture",
  );

  // E o shaper confirma que, apesar disso, eles DESENHAM (1 glifo).
  const shaped = shapedGlyphs();
  const desenham = resolvidosPorLigadura.filter((n) => resolvesToSingleGlyph(shaped, n));
  assert.deepEqual(
    desenham.sort(),
    [...resolvidosPorLigadura].sort(),
    "o shaper deixou de resolver estes nomes — aí sim seria defeito real",
  );

  // A normalização precisa continuar produzindo exatamente esses textos.
  for (const n of resolvidosPorLigadura) {
    assert.equal(a2uiIconLigature(n), n, `a normalização de ${n} mudou de forma inesperada`);
  }
});

// ─── Nota sobre o mapa de aliases (histórico, NÃO é asserção) ───
//
// Durante este trabalho a decisão sobre os alvos de favoriteOff/starOff mudou
// duas vezes, e por isso NÃO existe aqui nenhuma asserção de "decisão de
// produto": um teste que congela uma escolha de produto vira um teste que
// INDUZ REGRESSÃO quando a escolha muda — foi o que quase aconteceu.
//
// O que o teste mede é só o que o R7 exige: que cada nome do catálogo (com o
// alias aplicado) vire UM glifo no shaper. Qual é o alvo semanticamente correto
// é decisão de produto, registrada no comentário de frontend/js/a2ui-icon-names.js.
//
// Registro factual do que foi medido (útil para quem for mexer no mapa):
//   * favorite_off / star_off / play / rewind NÃO são ligaduras do GSUB — sem
//     alias, saem como TEXTO (é a razão de o mapa existir);
//   * favorite_border e star_border SÃO ligaduras, e shapeiam para o MESMO
//     glifo de favorite/star. Texto diferente pode dar o mesmo desenho: quem
//     separa preenchido de contorno é o EIXO VARIÁVEL FILL, não o nome.
