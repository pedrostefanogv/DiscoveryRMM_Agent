// a2ui-icon-names.js — normalização dos nomes de glifo do componente Icon do A2UI.
//
// POR QUE EXISTE (achado R7):
// O Icon do catálogo A2UI usa uma lista FECHADA de 59 nomes camelCase
// (ICON_NAMES, em @a2ui/web_core/src/v0_9/basic_catalog/components/
// basic_components.js). Com a webfont servida localmente, o glifo é resolvido
// por LIGADURA: o texto "shopping_cart" é SUBSTITUÍDO pelo glifo do ícone.
//
// ═══ COMO MEDIR ISTO (armadilha real — leia antes de mexer no mapa) ═══
// O mecanismo desta fonte é LIGADURA, e há DUAS grandezas diferentes que é
// fácil confundir:
//
//   (A) NOMES DE GLIFO  — font.getGlyphOrder() ... 6683 entradas.
//       É o nome INTERNO de cada desenho. NÃO é o vocabulário da ligadura.
//   (B) LIGADURAS       — o TEXTO que o GSUB (rlig, lookup type 4) reconhece
//       e troca por um glifo ............ 4319 entradas.
//   E o cmap tem 4425 entradas (o mapa caractere -> glifo usado como
//   componente das ligaduras).
//
// Medir com (A) dá FALSO NEGATIVO: "favorite_border" NÃO está em
// getGlyphOrder(), mas É uma ligadura real e funciona. Do mesmo modo
// "location_on" não é nome de glifo, mas a ligadura resolve para o glifo
// "place". As duas perguntas parecem a mesma e não são:
//     (A) "existe um glifo CHAMADO assim?"
//     (B) "o GSUB reconhece esse TEXTO?"
// O critério CORRETO é (B).
//
// ═══ O MECANISMO: 'rlig', NÃO 'liga' ═══
// Medido alternando as features no shaper (uharfbuzz; converta o woff2 com
// fontTools flavor=None, pois o HarfBuzz não lê woff2):
//     default                  -> 'arrow_back' resolvido
//     liga=False               -> 'arrow_back' resolvido  (NADA muda: 'liga' é INERTE)
//     rlig=False               -> vira texto cru
//     rlig=False,rclt=False    -> vira texto cru           ('rclt' não tem lookups)
// FeatureTags do GSUB: ['rclt','rlig'] — não existe 'liga'. O 'rlig' é REQUIRED
// e aplicado por padrão em DFLT/latn, então o navegador SEMPRE o aplica. O
// font-feature-settings:"liga" do CSS é intenção documentada, não o mecanismo.
//
// ═══ ESTADO REAL DOS 59 NOMES: TODOS RESOLVEM ═══
// Com o critério (B), os 59 nomes do catálogo resolvem (camelCase -> snake_case
// -> ALIASES -> ligadura). Os 4 aliases abaixo existem porque estes 4 textos
// NÃO são ligaduras do GSUB (verificado lendo os 4319 textos de ligadura):
//     favorite_off   star_off   play   rewind
// Os nomes com '_border' (favorite_border, star_border, bookmark_border) SÃO
// ligaduras e são justamente as variantes de CONTORNO do Material.
//
// ═══ ARMADILHA REGISTRADA (não repetir) ═══
// Nesta fonte, TEXTO DIFERENTE PODE DAR O MESMO GLIFO. Medido:
//     favorite        -> LigGlyph 'favorite'  (gid 4174)
//     favorite_border -> LigGlyph 'favorite'  (gid 4174)   <-- MESMO glifo
//     star / star_border -> 'star'  ;  bookmark / bookmark_border -> 'bookmark'
// Ou seja, o texto "_border" NÃO seleciona um desenho de contorno: quem
// controla preenchido vs contorno é o EIXO VARIÁVEL FILL (fvar: 0..1), com os
// glifos 'favorite.fill' / 'star.fill'. Como o app fixa FILL 0 numa regra única
// (ver styles/icon-font.css), as variantes "_border" e o nome base renderizam
// IGUAL hoje. Isso NÃO é motivo para trocar o alias: favorite_border é a
// ligadura semanticamente correta para "off" e mantém o mapa alinhado ao
// vocabulário oficial da fonte.
(function () {
  "use strict";

  // Aliases: nome canônico do catálogo -> ligadura REAL da fonte.
  // Os 4 destinos são ligaduras confirmadas lidas do GSUB (não getGlyphOrder).
  var ALIASES = {
    favorite_off: "favorite_border",
    star_off: "star_border",
    play: "play_arrow",
    rewind: "fast_rewind",
  };

  // a2uiIconLigature converte o nome vindo do modelo na ligadura da fonte.
  //   - aceita camelCase ("arrowBack"), snake_case ("arrow_back"),
  //     kebab-case ("arrow-back") e variações de caixa ("ARROW_BACK");
  //   - devolve "" quando não há como resolver, para o chamador decidir.
  function a2uiIconLigature(raw) {
    if (raw === null || raw === undefined) return "";
    var s = String(raw).trim();
    if (!s) return "";
    // camelCase -> snake_case ("arrowBack" -> "arrow_back").
    // Preserva dígitos ("forward10" -> "forward_10") e não mexe no que já tem
    // separador.
    if (s.indexOf("_") === -1 && s.indexOf("-") === -1 && s.indexOf(" ") === -1) {
      s = s.replace(/([a-z0-9])([A-Z])/g, "$1_$2");
    }
    s = s.replace(/[-\s]+/g, "_").toLowerCase();
    s = s.replace(/__+/g, "_").replace(/^_|_$/g, "");
    if (!s) return "";
    return ALIASES[s] || s;
  }

  window.A2uiIconNames = {
    a2uiIconLigature: a2uiIconLigature,
    ALIASES: ALIASES,
  };
})();
