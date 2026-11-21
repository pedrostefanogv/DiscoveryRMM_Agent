import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import vm from "node:vm";

const HERE = fileURLToPath(new URL(".", import.meta.url));
const APP_CHAT_JS = join(HERE, "..", "..", "..", "frontend", "js", "app-chat.js");
const source = readFileSync(APP_CHAT_JS, "utf8");

function extractFunction(src, name) {
  const marker = "function " + name + "(";
  const start = src.indexOf(marker);
  assert.ok(start >= 0, `function ${name} not found in app-chat.js`);
  let i = src.indexOf("{", start);
  assert.ok(i > start, `body not found for ${name}`);
  let depth = 0;
  for (; i < src.length; i++) {
    if (src[i] === "{") depth++;
    else if (src[i] === "}") {
      depth--;
      if (depth === 0) return src.slice(start, i + 1);
    }
  }
  assert.fail(`unbalanced braces in ${name}`);
}

function makeEl(tag = "div") {
  const classes = new Set();
  const children = [];
  const attrs = {};
  const el = {
    tagName: tag.toUpperCase(),
    className: "",
    dataset: {},
    children,
    parentNode: null,
    textContent: "",
    setAttribute(k, v) { attrs[k] = String(v); },
    getAttribute(k) { return k in attrs ? attrs[k] : null; },
    appendChild(child) {
      child.parentNode = el;
      children.push(child);
      return child;
    },
    remove() {
      if (!el.parentNode) return;
      const idx = el.parentNode.children.indexOf(el);
      if (idx >= 0) el.parentNode.children.splice(idx, 1);
      el.parentNode = null;
    },
    querySelector() { return null; },
    classList: {
      add(...names) { names.forEach((n) => classes.add(n)); },
      remove(...names) { names.forEach((n) => classes.delete(n)); },
      toggle(n, on) { if (on) classes.add(n); else classes.delete(n); },
      contains(n) { return classes.has(n); },
    },
  };
  return el;
}

function createHarness() {
  const chatMessagesEl = makeEl("div");
  const handles = [];
  const userBubbles = [];
  const actionsSent = [];
  let offlineBannerCalls = 0;

  const fns = [
    "formatA2uiActionLabel",
    "a2uiSetBusyEntry",
    "a2uiSetBusy",
    "clearA2uiBusy",
    "a2uiClearSurfaceWatchdog",
    "a2uiArmSurfaceWatchdog",
    "a2uiDestroyEntry",
    "a2uiSurfaceDestroy",
    "a2uiArchiveSurface",
    "a2uiIndexComponentLabels",
    "a2uiWarnUnsupportedComponents",
    "onChatA2ui",
    "ensureA2uiSurface",
    "applyLocalA2uiAction",
    "handleA2uiUserAction",
    "clearA2uiSurface",
  ].map((n) => extractFunction(source, n)).join("\n\n");

  const context = {
    console,
    setTimeout,
    clearTimeout,
    document: {
      createElement: (tag) => makeEl(tag),
    },
    window: {
      A2uiChat: {
        catalog: { components: new Set(["Column", "Row", "Text", "Button", "Icon"]) },
        createSurface(contentEl, surfaceId) {
          let actionCb = null;
          const processed = [];
          const dataModel = Object.create(null);
          const h = {
            surfaceId,
            processed,
            destroyed: false,
            onUserAction(cb) { actionCb = cb; },
            triggerAction(action) { if (actionCb) actionCb(action); },
            processMessages(msgs) { processed.push(...msgs); },
            updateDataModel(path, val) { dataModel[path] = val; },
            getDataModel(path) { return dataModel[path]; },
            destroy() { h.destroyed = true; },
          };
          handles.push(h);
          return h;
        },
      },
    },
    chatMessagesEl,
    CHAT_TOOL_LABELS: {
      get_inventory: "Coletando inventário",
      get_performance_snapshot: "Capturando desempenho",
    },
    A2UI_SURFACE_WATCHDOG_MS: 12000,
    A2UI_ACTION_DEDUPE_MS: 1200,
    A2UI_LOCAL_ACTION_PREFIX: "ui.",
    a2uiLastActionKey: "",
    a2uiLastActionAt: 0,
    a2uiSurfaces: Object.create(null),
    a2uiArchivedSurfaces: [],
    pendingA2uiAction: 0,
    chatSending: false,
    chatOfflineActive: false,
    showChatOfflineBannerNow() { offlineBannerCalls++; },
    a2uiRendered: false,
    a2uiThemeAttach() {},
    a2uiThemeDispose() {},
    a2uiThemeRefresh() {},
    syncColorMode() {},
    translate(k) { return k; },
    scheduleChatScrollToBottom() {},
    fallbackA2uiToMarkdown() {},
    isLocalA2uiAction(name) { return String(name || "").startsWith("ui."); },
    appApi() {
      return {
        AnswerA2uiAction(payload) { actionsSent.push(JSON.parse(payload)); },
      };
    },
    addChatMessage(role, content) {
      const div = makeEl("div");
      div.className = "chat-msg " + role;
      div.textContent = content;
      chatMessagesEl.appendChild(div);
      userBubbles.push(div);
      return div;
    },
    sendChatMessageWithA2uiAction() {},
  };

  vm.runInNewContext(fns, context);
  return {
    ctx: context,
    chatMessagesEl,
    handles,
    userBubbles,
    actionsSent,
    getOfflineBannerCalls: () => offlineBannerCalls,
  };
}

test("duplicate createSurface with same surfaceId archives first card and appends new card at end", () => {
  const { ctx, chatMessagesEl, handles } = createHarness();

  ctx.onChatA2ui(JSON.stringify({ version: "v0.9", createSurface: { surfaceId: "capabilities_card" } }));
  ctx.onChatA2ui(JSON.stringify({
    version: "v0.9",
    updateComponents: {
      surfaceId: "capabilities_card",
      components: [
        { id: "root", component: "Column", children: ["b1"] },
        { id: "b1Label", component: "Text", text: "Inventário" },
        { id: "b1", component: "Button", child: "b1Label", action: { event: { name: "get_inventory", context: {} } } },
      ],
    },
  }));

  // Simulate user message / response between the two cards.
  ctx.addChatMessage("user", "▶ Inventário");

  ctx.onChatA2ui(JSON.stringify({ version: "v0.9", createSurface: { surfaceId: "capabilities_card" } }));
  ctx.onChatA2ui(JSON.stringify({
    version: "v0.9",
    updateComponents: {
      surfaceId: "capabilities_card",
      components: [
        { id: "root", component: "Column", children: ["b2"] },
        { id: "b2Label", component: "Text", text: "Desempenho" },
        { id: "b2", component: "Button", child: "b2Label", action: { event: { name: "get_performance_snapshot", context: {} } } },
      ],
    },
  }));

  assert.equal(handles.length, 2, "must create two distinct surface handles");
  assert.equal(chatMessagesEl.children.length, 3, "DOM order: card 1, user bubble, card 2");
  assert.equal(chatMessagesEl.children[0].dataset.surfaceId, "capabilities_card");
  assert.equal(chatMessagesEl.children[1].textContent, "▶ Inventário");
  assert.equal(chatMessagesEl.children[2].dataset.surfaceId, "capabilities_card");

  assert.equal(chatMessagesEl.children[0].classList.contains("a2ui-superseded"), true);
  assert.equal(chatMessagesEl.children[2].classList.contains("a2ui-superseded"), false);
  assert.equal(handles[0].processed.length, 1, "first card keeps its own updateComponents");
  assert.equal(handles[1].processed.length, 1, "second card receives only the new updateComponents");
  assert.equal(ctx.a2uiArchivedSurfaces.length, 1);
});

test("updateComponents without createSurface updates existing surface in-place", () => {
  const { ctx, chatMessagesEl, handles } = createHarness();

  ctx.onChatA2ui(JSON.stringify({ version: "v0.9", createSurface: { surfaceId: "wizard" } }));
  ctx.onChatA2ui(JSON.stringify({
    version: "v0.9",
    updateComponents: {
      surfaceId: "wizard",
      components: [{ id: "root", component: "Column", children: [] }],
    },
  }));
  ctx.onChatA2ui(JSON.stringify({
    version: "v0.9",
    updateComponents: {
      surfaceId: "wizard",
      components: [{ id: "root", component: "Column", children: ["step2"] }, { id: "step2", component: "Text", text: "Etapa 2" }],
    },
  }));

  assert.equal(handles.length, 1, "must reuse the same surface handle");
  assert.equal(chatMessagesEl.children.length, 1, "must keep a single bubble");
  assert.equal(handles[0].processed.length, 2, "both updates go to the same handle");
  assert.equal(ctx.a2uiArchivedSurfaces.length, 0);
});

test("clearA2uiSurface destroys both active and archived surfaces", () => {
  const { ctx, chatMessagesEl, handles } = createHarness();

  ctx.onChatA2ui(JSON.stringify({ version: "v0.9", createSurface: { surfaceId: "card" } }));
  ctx.onChatA2ui(JSON.stringify({
    version: "v0.9",
    updateComponents: { surfaceId: "card", components: [{ id: "root", component: "Column" }] },
  }));
  ctx.onChatA2ui(JSON.stringify({ version: "v0.9", createSurface: { surfaceId: "card" } }));
  ctx.onChatA2ui(JSON.stringify({
    version: "v0.9",
    updateComponents: { surfaceId: "card", components: [{ id: "root", component: "Column" }] },
  }));

  ctx.clearA2uiSurface();

  assert.equal(handles.every((h) => h.destroyed), true);
  assert.equal(chatMessagesEl.children.length, 0);
  assert.equal(ctx.a2uiArchivedSurfaces.length, 0);
  assert.deepEqual(Object.keys(ctx.a2uiSurfaces), []);
});

test("formatA2uiActionLabel resolves button text, CHAT_TOOL_LABELS, and prettified fallback", () => {
  const { ctx } = createHarness();

  const entry = {
    componentLabels: {
      btn1: "Inventário",
    },
  };

  assert.equal(
    ctx.formatA2uiActionLabel({ name: "get_inventory", sourceComponentId: "btn1" }, entry),
    "▶ Inventário",
  );
  assert.equal(
    ctx.formatA2uiActionLabel({ name: "get_performance_snapshot", sourceComponentId: "unknown" }, entry),
    "▶ Capturando desempenho",
  );
  assert.equal(
    ctx.formatA2uiActionLabel({ name: "custom_action_name" }, null),
    "▶ custom action name",
  );
});

test("handleA2uiUserAction blocks server action when offline but keeps local ui.* working", () => {
  const { ctx, chatMessagesEl, handles, actionsSent, getOfflineBannerCalls } = createHarness();
  let localApplied = 0;
  ctx.applyLocalA2uiAction = () => { localApplied++; };

  ctx.onChatA2ui(JSON.stringify({ version: "v0.9", createSurface: { surfaceId: "card_off" } }));
  ctx.onChatA2ui(JSON.stringify({
    version: "v0.9",
    updateComponents: { surfaceId: "card_off", components: [{ id: "root", component: "Column" }] },
  }));

  ctx.chatOfflineActive = true;
  handles[0].triggerAction({ name: "get_inventory", sourceComponentId: "b1", context: {} });

  assert.equal(getOfflineBannerCalls(), 1);
  assert.equal(actionsSent.length, 0);
  assert.equal(chatMessagesEl.children.length, 1, "no user bubble appended while offline");
  assert.equal(chatMessagesEl.children[0].classList.contains("a2ui-busy"), false);

  handles[0].triggerAction({ name: "ui.next", context: { target: "/etapa" } });
  assert.equal(localApplied, 1, "local ui.* action still executes offline");
});

test("a2uiIndexComponentLabels resolves nested Row/Text and live { path } bindings", () => {
  const { ctx, handles, userBubbles } = createHarness();

  ctx.onChatA2ui(JSON.stringify({ version: "v0.9", createSurface: { surfaceId: "nested_card" } }));
  ctx.onChatA2ui(JSON.stringify({
    version: "v0.9",
    updateComponents: {
      surfaceId: "nested_card",
      components: [
        { id: "root", component: "Column", children: ["btnNested", "btnDynamic"] },
        { id: "btnNested", component: "Button", child: "btnRow", action: { event: { name: "printer", context: {} } } },
        { id: "btnRow", component: "Row", children: ["ico", "lblNested"] },
        { id: "ico", component: "Icon", name: "print" },
        { id: "lblNested", component: "Text", text: "Listar impressoras" },
        { id: "btnDynamic", component: "Button", child: "lblDynamic", action: { event: { name: "step_done", context: {} } } },
        { id: "lblDynamic", component: "Text", text: { path: "/btnText" } },
      ],
    },
  }));

  handles[0].updateDataModel("/btnText", "Concluir etapa 2");
  handles[0].triggerAction({ name: "printer", sourceComponentId: "btnNested", context: {} });
  handles[0].triggerAction({ name: "step_done", sourceComponentId: "btnDynamic", context: {} });

  assert.equal(userBubbles[0].textContent, "▶ Listar impressoras");
  assert.equal(userBubbles[1].textContent, "▶ Concluir etapa 2");
});
