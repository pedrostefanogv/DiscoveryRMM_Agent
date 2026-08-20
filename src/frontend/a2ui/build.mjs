// build.mjs — Gera o bundle A2UI (IIFE) commitado em frontend/a2ui-bundle.js.
//
// O frontend do Discovery é vanilla JS (sem bundler em runtime). O renderer
// A2UI (@a2ui/lit + @a2ui/web_core) é distribuído como pacotes npm ESM, então
// precisamos de um passo de build (esbuild) que produz um único arquivo IIFE
// que expõe `window.A2uiChat`. O artefato gerado é COMMITADO no repo — o
// runtime do agente não depende de node/npm.
//
// Uso:
//   node build.mjs            → build único
//   node build.mjs --watch    → rebuild em mudanças (dev)
//
// Saída: ../a2ui-bundle.js (relativo a este arquivo → frontend/a2ui-bundle.js)

import { build, context } from "esbuild";
import { fileURLToPath } from "node:url";
import path from "node:path";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const outfile = path.resolve(__dirname, "..", "a2ui-bundle.js");

const options = {
  entryPoints: [path.resolve(__dirname, "entry.js")],
  outfile,
  bundle: true,
  format: "iife",
  globalName: "A2uiChat",
  target: ["es2020"],
  minify: false,
  sourcemap: false,
  logLevel: "info",
  // Garante que o bundle expõe window.A2uiChat. Com format:"iife" +
  // globalName, o esbuild gera `var A2uiChat = (()=>{...})()` (variável
  // local), que NÃO cria window.A2uiChat. O footer abaixo força a atribuição
  // global, necessária para o app-chat.js consumir window.A2uiChat.
  footer: {
    js: "window.A2uiChat = A2uiChat;",
  },
};

const watch = process.argv.includes("--watch");

if (watch) {
  const ctx = await context(options);
  await ctx.watch();
  console.log(`[a2ui] watch ativo → ${outfile}`);
} else {
  await build(options);
  console.log(`[a2ui] bundle gerado → ${outfile}`);
}