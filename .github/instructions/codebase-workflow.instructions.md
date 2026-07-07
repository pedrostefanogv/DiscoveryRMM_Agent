---
applyTo: '**'
---

## MANDATORY: Always use Codebase Memory MCP to read the codebase

**This rule applies to EVERY request that involves this codebase.**

## Build — SEMPRE usar Wails

**NUNCA use `go build` diretamente.** O frontend (JS/HTML/CSS + wailsjs) é embedado via `//go:embed` e processado exclusivamente pelo Wails CLI.

Comando correto para build:
```powershell
cd src
wails build -o bin\discovery-agent.exe
```

Dev mode com hot-reload:
```powershell
cd src
wails dev
```

`go build` compila o Go mas **NÃO** processa os assets do frontend — o binário gerado não terá a interface.

### Rules

1. **Call `mcp_codebase-memo_get_architecture` FIRST** — before writing code, editing files, or answering any question about the codebase.
2. Use the returned context to make targeted, accurate changes.
3. **Do NOT use** `grep_search`, `file_search`, `semantic_search`, or `read_file` for initial codebase exploration.
4. Re-query only if additional context is needed during implementation.

### Workflow

```
mcp_codebase-memo_get_architecture({ "project": "<project>" })   // start here
mcp_codebase-memo_search_graph({ "name_pattern": "<symbol>" })   // find symbols
mcp_codebase-memo_get_code_snippet({ "qualified_name": "<fn>" }) // read code
```

### Why

- Pre-built index covers the entire codebase with relevance ranking.
- Faster and more accurate than manual file search.
- Prevents reading stale files or following ghost references.
