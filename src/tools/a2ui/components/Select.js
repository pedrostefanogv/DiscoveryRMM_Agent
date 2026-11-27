// Select.js — componente de DROPDOWN do catálogo A2UI do Discovery.
//
// Por que existe: o basic catalog v0.9 não tem dropdown/combobox. O único
// componente de seleção é ChoicePicker, que renderiza as opções como LISTA
// VISÍVEL (radio/checkbox) — por isso o agente prometia "menu dropdown" e o
// usuário via uma lista aberta (chat.db #591, 2026-10-08).
//
// Aqui o componente é registrado no MESMO catalogId do basic (o createSurface
// do LLM continua mandando basic_catalog.json). Para o servidor aceitar, o nome
// "Select" precisa estar em AiChatA2uiValidator.KnownComponents e no prompt.
import { html, nothing } from "lit";
import { z } from "zod";
import {
  AccessibilityAttributesSchema,
  DynamicStringSchema,
} from "@a2ui/web_core/v0_9";
import { A2uiController, A2uiLitElement } from "@a2ui/lit/v0_9";

export const SelectApi = {
  name: "Select",
  schema: z
    .object({
      accessibility: AccessibilityAttributesSchema.optional(),
      weight: z.number().optional(),
      label: DynamicStringSchema.describe("The label shown above the dropdown.").optional(),
      value: DynamicStringSchema.describe(
        "The currently selected value. Should be bound to a string in the data model.",
      ).optional(),
      placeholder: DynamicStringSchema.describe(
        "Text shown when no option is selected yet.",
      ).optional(),
      options: z
        .array(
          z
            .object({
              label: DynamicStringSchema.describe("The text to display for this option."),
              value: z.string().describe("The stable value associated with this option."),
            })
            .strict(),
        )
        .describe("The list of available options."),
    })
    .strict()
    .describe("A dropdown that lets the user pick a single option from a list."),
};

let nextSelectId = 0;

export class A2uiSelectElement extends A2uiLitElement {
  constructor() {
    super();
    this._inputId = `a2ui-select-input-${++nextSelectId}`;
  }

  createController() {
    return new A2uiController(this, SelectApi);
  }

  render() {
    const props = this.controller.props;
    if (!props) return nothing;
    // Tolera o modelo mandar a seleção como LISTA (cópia do ChoicePicker):
    // sem isso o valor era descartado e o dropdown voltava para o placeholder.
    const raw = props.value;
    const current = Array.isArray(raw)
      ? String(raw[0] ?? "")
      : typeof raw === "string"
        ? raw
        : "";
    const options = Array.isArray(props.options) ? props.options : [];
    const ariaLabel = props.accessibility?.label || props.label || "Seleção";
    return html`
      <div class="a2ui-select">
        ${props.label ? html`<label for=${this._inputId}>${props.label}</label>` : nothing}
        <select
          id=${this._inputId}
          class="a2ui-select-input"
          aria-label=${ariaLabel}
          ?disabled=${Boolean(props.disabled)}
          .value=${current}
          @change=${(e) => props.setValue && props.setValue(e.target.value)}
        >
          <option value="" ?selected=${current === ""}>${props.placeholder || "Selecione..."}</option>
          ${options.map(
            (opt) => html`<option value=${opt.value} ?selected=${String(opt.value) === current}>${opt.label != null ? opt.label : opt.value}</option>`,
          )}
        </select>
      </div>
    `;
  }
}

// Registro sem decorator (o build é ESM puro via esbuild).
if (!customElements.get("a2ui-select")) {
  customElements.define("a2ui-select", A2uiSelectElement);
}

export const A2uiSelect = {
  ...SelectApi,
  tagName: "a2ui-select",
};
