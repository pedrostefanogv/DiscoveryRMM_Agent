# Discovery Agent (Wails + Go)

Agente desktop/servico para inventario, automacao e distribuicao P2P em ambientes Windows, com UI local em Wails e modo headless via Windows Service.

## Repositórios Relacionados

- Agent (este repositório): https://github.com/pedrostefanogv/DiscoveryRMM_Agent
- Servidor de API: https://github.com/pedrostefanogv/DiscoveryRMM_API

## Capacidades Atuais

- Execucao em dois modos:
	- GUI desktop (Wails)
	- Serviço Windows headless (`--service`)
- Inventario de hardware/software com integracao osquery e fallback PowerShell.
- App store e operações de pacote (install/remove/upgrade) com integração winget.
- Automacao com politicas, execucao de tarefas e coleta de resultados.
- P2P com descoberta local/libp2p, onboarding e distribuicao de artefatos.
- Chat/IA integrado com ferramentas MCP internas.
- Update do agente e fluxo de bootstrap via instalador NSIS.

## Estrutura do Projeto

- `src/main.go`: entrypoint (GUI, service e flags de startup).
- `src/app`: domínio principal do agente (P2P, sync, bridges e runtime).
- `src/internal`: serviços internos (automação, inventário, service, update, mcp).
- `src/frontend`: UI HTML/CSS/JS do Wails.
- `src/build/windows/installer`: arquivos NSIS do instalador.
- `DOCs`: documentacao tecnica e operacional.

## Requisitos de Build Local

- Windows 10+.
- Go 1.23+.
- NSIS (makensis) no PATH.
- Opcional para build GUI: CLI do Wails.

## Build Local (Agente)

```powershell
# raiz do repositório
Set-Location .\src
go mod tidy
go test ./...
go build ./...
```

## Build Automatizado do Instalador (Padrão)

Script criado para gerar instalador padrao do agent (servico + discovery/p2p habilitado por padrao):

```powershell
# raiz do repositório
.\build\scripts\build-install-installer.ps1 -ProjectRoot $PWD
```

Saida esperada:

- Binario do agent: `src/build/bin/discovery.exe`
- Instalador: `src/build/bin/discovery-agent-install.exe`

Parâmetros úteis:

```powershell
.\build\scripts\build-install-installer.ps1 `
	-ProjectRoot $PWD `
	-OutputName discovery-agent-acme.exe `
	-DefaultUrl api.exemplo.com `
	-DefaultKey <token> `
	-DiscoveryEnabled 1 `
	-MinimalDefault
```

## Build Bootstrap (Online)

Script para gerar bootstrapper (baixa segunda etapa e executa instalador completo):

```powershell
.\build\scripts\build-bootstrap-installer.ps1 `
	-ProjectRoot $PWD `
	-PayloadUrl "https://cdn.exemplo.com/discovery-agent-install.exe" `
	-PayloadSha256 "<sha256-opcional>"
```

## Automacao de Build no GitHub Actions

Workflows adicionados:

- `.github/workflows/build-agent-installer.yml`
- `.github/workflows/build-agent-bootstrap.yml`
- `.github/workflows/release-agent-on-tag.yml`

Execucao:

1. Acesse Actions no GitHub.
2. Selecione `Build Agent Installer` para o instalador padrao ou `Build Agent Bootstrap Installer` para o bootstrap.
3. Execute via `Run workflow` preenchendo os parametros opcionais.
4. Baixe o artefato gerado (`discovery-agent-installer` ou `discovery-agent-bootstrap`).

## Release Automatizado por Tag

Quando uma tag Git no formato abaixo for enviada ao GitHub, o workflow `release-agent-on-tag.yml` faz o build do agent e publica os executaveis automaticamente em GitHub Releases:

- Release normal: `v1.2.3`
- Beta: `v1.2.3-beta.1`
- LTS: `v1.2.3-lts.1`

Comportamento:

- `v1.2.3`: release estavel normal.
- `v1.2.3-beta.1`: release marcada como prerelease.
- `v1.2.3-lts.1`: release LTS sem promover como latest.

O workflow utiliza a propria tag como versao embutida no binario via ldflags e publica os artefatos em GitHub Releases.

## Observacao sobre Operacao em Rede

O instalador padrao ja registra e inicia o Windows Service do agent e mantem discovery/p2p habilitado para operacao em rede local. A comunicacao de entrada pode depender de regra de firewall da maquina/ambiente.

## Documentação

- Mapa de documentacao: `DOCs/README.md`
- Guia operacional de instalador (ponteiro atual): `DOCs/GUIA_INSTALADOR_OFFLINE_ONLINE_API.md`
- Arquitetura detalhada: `ARCHITECTURE.md`
