// entry.js — Ponto de entrada do bundle A2UI.
//
// Expõe `window.A2uiChat` com uma API mínima para o frontend vanilla:
//   - A2uiChat.createSurface(containerEl, surfaceId) → cria uma surface
//     renderizada dentro de containerEl e retorna um handle.
//   - handle.processMessages(messages) → alimenta o MessageProcessor com
//     mensagens A2UI (createSurface/updateComponents/updateDataModel/...).
//   - handle.onUserAction(cb) → registra callback para eventos userAction
//     (cliques/inputs em componentes).
//   - handle.destroy() → remove a surface e libera recursos.
//
// O bundle é gerado por esbuild (IIFE) e commitado em frontend/a2ui-bundle.js
// (gere com: cd src/tools/a2ui && npm install && npm run build).
// O runtime do agente não depende de node/npm.

import { MessageProcessor, Catalog } from "@a2ui/web_core/v0_9";
import { A2uiSurface, basicCatalog } from "@a2ui/lit/v0_9";
import { A2uiSelect } from "./components/Select.js";

// Catálogo do Discovery = basic v0.9 + componentes próprios (Select/dropdown).
// O id é o MESMO do basic de propósito: o createSurface emitido pelo LLM
// continua mandando basic_catalog.json e o AiChatA2uiValidator do servidor não
// precisa conhecer um catalogId novo — só o nome do componente (Select).
const catalog = new Catalog(
  basicCatalog.id,
  [...basicCatalog.components.values(), A2uiSelect],
  [...basicCatalog.functions.values()],
);

const CATALOG_ID = catalog.id;

function createSurface(containerEl, surfaceId) {
  if (!containerEl) {
    throw new Error("A2uiChat.createSurface: containerEl é obrigatório");
  }
  const surfaceIdFinal = surfaceId || "discovery-chat-surface";

  const userActionHandlers = [];

  // O MessageProcessor recebe o actionHandler no CONSTRUTOR. NÃO existe
  // `processor.events`: a chamada antiga lançava
  // "Cannot read properties of undefined (reading 'subscribe')" e derrubava o
  // createSurface inteiro — a surface era criada, mas o handle nunca era
  // devolvido, então NENHUM card era exibido (o erro ficava engolido pelo
  // catch do ensureA2uiSurface no app-chat.js).
  const processor = new MessageProcessor([catalog], (action) => {
    for (const handler of userActionHandlers) {
      try {
        handler(action);
      } catch (e) {
        console.error("[a2ui] userAction handler error:", e);
      }
    }
  });

  // Cria a surface assim que o processor estiver pronto.
  processor.onSurfaceCreated((s) => {
    if (s.id !== surfaceIdFinal) return;
    const host = document.createElement("div");
    host.className = "a2ui-surface-host";
    containerEl.appendChild(host);
    // A2uiSurface é um custom element Lit: o construtor NÃO recebe a surface.
    // O model precisa ser atribuído na propriedade `surface` (com
    // `new A2uiSurface(s)` o elemento renderiza vazio — `render()` devolve
    // nothing porque `this.surface` fica undefined).
    const surface = new A2uiSurface();
    surface.surface = s;
    host.appendChild(surface);
  });

  // Envia a mensagem createSurface ao processor para que a surface seja criada.
  // Sem isso, o MessageProcessor nunca cria a surface e mensagens subsequentes
  // (updateComponents/updateDataModel) falham silenciosamente.
  processor.processMessages([
    { version: "v0.9", createSurface: { surfaceId: surfaceIdFinal, catalogId: CATALOG_ID } },
  ]);

  return {
    surfaceId: surfaceIdFinal,
    processMessages(messages) {
      processor.processMessages(messages);
    },
    onUserAction(cb) {
      if (typeof cb === "function") userActionHandlers.push(cb);
    },
    destroy() {
      try {
        processor.processMessages([
          { version: "v0.9", deleteSurface: { surfaceId: surfaceIdFinal } },
        ]);
      } catch (_) {
        // ignore
      }
      containerEl.innerHTML = "";
    },
  };
}

// Expõe a API global (o esbuild com globalName="A2uiChat" cria window.A2uiChat).
export { createSurface, catalog, CATALOG_ID };
