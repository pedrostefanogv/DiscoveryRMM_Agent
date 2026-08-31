# PLANO — Correção de Janela Fora da Área Visível (DPI Scaling 125%/150%)

**Data:** 2026-08-30
**Escopo:** Discovery Agent (Windows, Wails v3)
**Status:** Implementado (fases 1 e 2) — fase 3 opcional

---

## 1. Problema

Em monitores 1080p com escala de DPI do Windows em **125% ou 150%**, a janela
do agente abre **fora da área de visualização**: o chrome customizado da
janela (botões minimizar/maximizar/fechar, localizado no topo via
`.window-chrome`) fica cortado/inacessível.

### Causa raiz

- A janela é criada com tamanho fixo `1280x860` (`src/app/app.go`, constantes
  `WindowWidth`/`WindowHeight`) e mínimo `980x700`.
- Em 1080p (1920x1080 físicos) com escala 150%, a área de trabalho lógica é
  `1920/1.5 = 1280` x `1080/1.5 = 720` DIP. A janela de 1280x860 lógicos
  **excede a altura disponível (720 DIP)** — o topo/baixo é cortado.
- Não havia nenhum mecanismo de clamp da janela à área de trabalho
  (`WorkArea`) da screen.

---

## 2. Solução implementada

### Fase 1 — Clamp no backend (Go)

**Novo arquivo: `src/app/window_fit.go`**

- `App.FitWindowToWorkArea()`: obtém a screen da janela via
  `mainWindow.GetScreen()` (Wails v3) e aplica `fitWindowToScreen`.
- `fitWindowToScreen`:
  1. Lê `screen.WorkArea` (unidades lógicas/DIP — o Wails converte
     automaticamente via `applyDPIScaling`, então não há conversão manual de
     DPI no nosso código).
  2. Se a janela excede `WorkArea - 2*16px` de margem, reduz o tamanho com
     `window.SetSize()`.
  3. Se a origem ou o canto direito/inferior estiverem fora da WorkArea,
     reposiciona com `window.SetPosition()`.
  4. É **idempotente**: se a janela já cabe, nada é feito (sem flicker).

**Integração em `src/main.go`:**

- Hooks registrados na janela:
  - `events.Common.WindowShow` → aplica clamp no primeiro show;
  - `events.Common.WindowDidResize` → cobre maximizar/restaurar;
  - `events.Common.WindowDPIChanged` → cobre arrastar entre monitores com
    escalas diferentes.
- O hook ignora janelas maximizadas/fullscreen (não interfere nesses estados).

**Integração em `src/app/app.go`:**

- `ShowMainWindow()` (single-instance / tray) também chama
  `FitWindowToWorkArea()` após mostrar a janela.

### Fase 2 — Rede de segurança no frontend

**`src/frontend/js/app-window.js`:**

- **Atalhos de teclado** (fallback caso o chrome fique inacessível):
  - `Ctrl+Alt+M` → maximizar/restaurar;
  - `Ctrl+Alt+W` → fechar para tray;
  - `Ctrl+Alt+D` → diagnóstico no console (inner/outer size,
    `devicePixelRatio`, visibilidade do chrome).
- **Recuperação automática:** `ensureChromeAccessible()` roda 1,5s após o
  boot; se `#windowChrome` estiver fora do viewport (janela cortada),
  maximiza a janela automaticamente para devolver o controle ao usuário.

**`src/frontend/wails-bridge.js`:**

- Novo helper `window.wails.maximiseWindow()` → `Window.Maximise()`.

---

## 3. Fase 3 (opcional) — Melhorias futuras

| #   | Melhoria                        | Descrição                                                                                                                                                                     | Esforço |
| --- | ------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Tamanho inicial adaptativo      | Calcular `WindowWidth/Height` no startup a partir da WorkArea da screen primária (ex.: `min(1280, workW-32)`), evitando o resize visível pós-show.                            | Médio   |
| 2   | Persistir geometria             | Salvar tamanho/posição da janela (config local) e restaurar com clamp na abertura.                                                                                            | Médio   |
| 3   | Menu do tray "Restaurar janela" | Item no menu do tray que chama `ShowMainWindow` + `FitWindowToWorkArea` (já coberto pelo `ShowMainWindow`, mas vale item explícito "Redefinir janela" que também centraliza). | Baixo   |
| 4   | Flag CLI `--reset-window`       | Reseta geometria persistida (útil para suporte remoto).                                                                                                                       | Baixo   |
| 5   | Teste automatizado              | ✅ **Concluído na revisão** — `src/app/window_fit_test.go` cobre: janela que cabe (no-op), clamp em tela pequena (1080p@150%), restauração de MinSize, reposicionamento (topo negativo e overflow à direita), WorkArea inválida e args nulos. | ✅ |

---

## 4. Como validar

1. **Reproduzir:** VM/PC com 1080p e escala 150% (Configurações → Tela → Escala).
2. **Build:** `cd src; wails build -o bin\discovery-agent.exe` (nunca `go build`).
3. **Cenários:**
   - Abrir o agente → janela deve caber totalmente na tela, com o chrome
     (min/max/close) visível no topo.
   - Verificar no log: `[window-fit] redimensionando janela ...` quando houver clamp.
   - Arrastar a janela para um monitor com escala diferente → clamp re-aplicado.
   - Maximizar/restaurar → sem interferência do clamp.
   - Segunda instância (`ShowMainWindow`) → janela visível e dentro da área.
   - Atalhos: `Ctrl+Alt+M`, `Ctrl+Alt+W`, `Ctrl+Alt+D`.
4. **Modo navegador (debug HTTP):** atalhos de teclado funcionam como no-op
   seguro (sem métodos de janela nativos).

---

## 5. Riscos e mitigação

- **Flicker no boot:** o clamp roda no `WindowShow`, então pode haver um
  resize visível no primeiro frame. Mitigação futura: fase 3 item 1
  (tamanho inicial adaptativo).
- **Monitores múltiplos:** `GetScreen()` retorna a screen onde a janela está;
  o clamp usa a WorkArea correta por monitor (DIP já convertido).
- **Fullscreen/maximizado:** protegido por check explícito no hook.

---

## 6. Revisão (2026-08-30) — bugs corrigidos e melhorias

### Bugs encontrados e corrigidos

1. **`SetSize` rebaixava o `MinSize` permanentemente** (crítico)
   - O Wails v3, ao receber `SetSize(w,h)` com `h < MinHeight`, **rebaixa o
     MinSize da janela** para o novo tamanho (`webview_window.go` →
     `SetMinSize`). No cenário 1080p@150% (clamp para ~656 de altura <
     MinHeight 700), o mínimo original (980x700) ficava permanentemente
     reduzido — em um monitor maior, o usuário não conseguiria redimensionar
     abaixo/acima corretamente.
   - **Correção:** após o clamp, se o novo tamanho respeita o mínimo
     original, `SetMinSize(WindowMinWidth, WindowMinHeight)` é restaurado.
     Quando o clamp é menor que o mínimo (tela pequena), não restauramos —
     `SetMinSize` redimensionaria de volta e desfaria o clamp.

2. **Reposicionamento usava tamanho antigo em cenários de clamp**
   - O cálculo de posição agora usa explicitamente `newW/newH` (tamanho
     pós-clamp), evitando posição incorreta quando `SetSize` acabou de ser
     chamado.

3. **`WindowDidResize` sem debounce**
   - O evento dispara dezenas de vezes durante resize interativo;
     `SetSize`/`SetPosition` usam `InvokeSync` (dispatch + wait na main
     thread), então o clamp "brigaria" com o arraste do usuário.
   - **Correção:** debounce de 150ms (`time.AfterFunc` + mutex) — o clamp só
     roda quando o resize estabiliza.

4. **Frontend: recuperação automática sem retry nem guard**
   - `ensureChromeAccessible` rodava uma única vez e podia maximizar
     indevidamente se o clamp do backend ainda não tivesse chegado, ou em
     modo navegador (sem métodos nativos).
   - **Correção:** retry com backoff (até 3 tentativas), guard para modo
     navegador e re-verificação após cada maximização.

### Melhorias estruturais

- **Interface `windowGeometry`** (`window_fit.go`): subconjunto testável da
  API de janela (assina como `application.Window`: `SetSize` retorna
  `Window`, `SetPosition` não retorna). Permite os testes unitários sem
  mockar a interface completa.
- **Testes:** `src/app/window_fit_test.go` — 7 casos, todos passando
  (`go test ./app/ -run TestFitWindow`).

### Verificação

- `go vet ./app/ ./` — limpo.
- `go test ./app/ -run TestFitWindow` — 7/7 PASS.
- `node --check frontend/js/app-window.js` — sintaxe OK.
