
## Build — SEMPRE usar o Wails CLI (v3)

**NUNCA use `go build` diretamente.** O frontend (JS/HTML/CSS + bindings) é embedado via `//go:embed` e processado exclusivamente pelo Wails CLI.

O projeto usa **Wails v3** (não existe mais `wails.json`; a configuração está em `src/build/config.yml` e as tarefas em `src/Taskfile.yml`). O CLI v2 (`wails build`) **falha** com "open ...wails.json: The system cannot find the file specified".

Comando correto para build (gera `src/bin/discovery-agent.exe` e `src/bin/discovery-service.exe`):
```powershell
cd src
wails3 build
```

Dev mode com hot-reload:
```powershell
cd src
wails3 dev -config ./build/config.yml
```

`go build` compila o Go mas **NÃO** processa os assets do frontend — o binário gerado não terá a interface.
