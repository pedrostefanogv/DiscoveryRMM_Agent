# Winget Store (Wails + Go)

Aplicativo desktop em Go com UI estilo loja para consumir o catalogo remoto e executar instalacao/remocao/atualizacao via winget.

## Etapa 1 (concluida): MVP funcional

- Leitura direta do catalogo JSON remoto.
- Exibicao em cards com busca local por nome/id/publisher/categoria.
- Acoes por app: instalar, remover, atualizar.
- Acao global: atualizar tudo.
- Consulta de apps instalados (`winget list`).
- Aba de inventario (hardware + sistema + softwares).
- Coleta por `osqueryi --json` quando disponivel, com fallback via PowerShell.
- Deteccao de `osquery` e botao de instalacao via `winget` (`osquery.osquery`) quando nao encontrado.
- Build desktop gerado em `build/bin/winget-store.exe`.

## Estrutura

- `main.go`: bootstrap do Wails.
- `app.go`: metodos expostos para a UI.
- `internal/models`: structs do catalogo.
- `internal/data`: client HTTP do catalogo.
- `internal/winget`: wrapper dos comandos winget.
- `internal/services`: servicos de dominio.
- `frontend`: UI estatica (HTML/CSS/JS).

## Requisitos

- Windows com `winget` disponivel no PATH.
- Go 1.23+ (testado com 1.26).
- CLI Wails em `%USERPROFILE%\\go\\bin\\wails.exe`.
- Opcional: osquery instalado (ex.: `C:\\Program Files\\osquery\\osqueryi.exe`).

## Comandos

```powershell
# no diretorio do projeto

go mod tidy
go build ./...

# build desktop (usa frontend estatico atual)
& "$env:USERPROFILE\go\bin\wails.exe" build -s -nopackage
```

## Etapa 2 (sugestao)

- Fila de tarefas com progresso por operacao (install/uninstall/upgrade).
- Parse estruturado da saida do winget para status amigavel.
- Mapa de apps instalados x catalogo para botao contextual (Instalar/Remover/Atualizar).
- Exportar inventario em JSON e CSV.

## Etapa 3 (sugestao)

- Persistencia local de preferencia/favoritos.
- Filtros por categoria/licenca/tags.
- Empacotamento NSIS e assinatura do binario.
