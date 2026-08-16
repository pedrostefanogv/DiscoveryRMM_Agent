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
// O bundle é gerado por esbuild (IIFE) e commitado em frontend/a2ui-bundle.js.
// O runtime do agente não depende de node/npm.

import { MessageProcessor } from "@a2ui/web_core/v0_9";
import { A2uiSurface, basicCatalog } from "@a2ui/lit/v0_9";

const CATALOG_ID = basicCatalog.id;

function createSurface(containerEl, surfaceId) {
  if (!containerEl) {
    throw new Error("A2uiChat.createSurface: containerEl é obrigatório");
  }
  const surfaceIdFinal = surfaceId || "discovery-chat-surface";

  const processor = new MessageProcessor([basicCatalog]);

  // Cria a surface assim que o processor estiver pronto.
  processor.onSurfaceCreated((s) => {
    if (s.id !== surfaceIdFinal) return;
    const host = document.createElement("div");
    host.className = "a2ui-surface-host";
    containerEl.appendChild(host);
    const surface = new A2uiSurface(s);
    host.appendChild(surface);
  });

  // Envia a mensagem createSurface ao processor para que a surface seja criada.
  // Sem isso, o MessageProcessor nunca cria a surface e mensagens subsequentes
  // (updateComponents/updateDataModel) falham silenciosamente.
  processor.processMessages([
    { version: "v0.9", createSurface: { surfaceId: surfaceIdFinal, catalogId: CATALOG_ID } },
  ]);

  const userActionHandlers = [];

  // Encaminha eventos do processor (userAction) para os handlers registrados.
  processor.events.subscribe((event) => {
    if (!event || !event.message || !event.message.userAction) return;
    const action = event.message.userAction;
    for (const handler of userActionHandlers) {
      try {
        handler(action);
      } catch (e) {
        console.error("[a2ui] userAction handler error:", e);
      }
    }
  });

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
export { createSurface, basicCatalog, CATALOG_ID };