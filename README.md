# Discovery Agent

PT_BR: agente Windows para inventario, automacao, distribuicao P2P, atualizacao e operacao local/remota, com interface Wails e execucao headless como Windows Service.

EN: Windows agent for inventory, automation, P2P distribution, update delivery and local/remote operation, with a Wails desktop UI and headless Windows Service mode.

## AI / VibeCode Notice

PT_BR: este repositorio contem partes desenvolvidas com apoio de fluxos assistidos por IA, incluindo VibeCode e ferramentas similares. Ao usar, estudar, copiar, contribuir ou redistribuir este codigo, voce concorda que deve revisar, validar e testar tudo antes de uso produtivo. Se sua politica interna restringe codigo assistido por IA, nao utilize este repositorio sem aprovacao formal.

EN: this repository contains parts developed with AI-assisted workflows, including VibeCode and similar tooling. By using, reviewing, copying, contributing to, or redistributing this code, you agree that all changes must be manually reviewed, validated and tested before production use. If your internal policy restricts AI-assisted code, do not use this repository without formal approval.

## Related Repositories / Repositorios Relacionados

- Agent: https://github.com/pedrostefanogv/DiscoveryRMM_Agent
- API Server / Servidor de API: https://github.com/pedrostefanogv/DiscoveryRMM_API

## Downloads / Downloads

- Stable release / Release estavel: https://github.com/pedrostefanogv/DiscoveryRMM_Agent/releases/tag/v1.0.0
- Beta release / Release beta: https://github.com/pedrostefanogv/DiscoveryRMM_Agent/releases/tag/v1.0.0-beta.1
- LTS release / Release LTS: https://github.com/pedrostefanogv/DiscoveryRMM_Agent/releases/tag/v1.0.0-lts.1
- All releases / Todas as releases: https://github.com/pedrostefanogv/DiscoveryRMM_Agent/releases

## Current Scope / Escopo Atual

- PT_BR: execucao em GUI desktop ou como servico Windows headless.
- EN: runs either as a desktop GUI or as a headless Windows service.
- PT_BR: inventario de hardware e software com integracao osquery e fallback PowerShell.
- EN: hardware and software inventory with osquery integration and PowerShell fallback.
- PT_BR: automacao operacional, execucao de tarefas e coleta de resultados.
- EN: operational automation, task execution and result collection.
- PT_BR: distribuicao P2P com descoberta local, onboarding e replicacao de artefatos.
- EN: P2P distribution with local discovery, onboarding and artifact replication.
- PT_BR: integracao com chat/IA e ferramentas MCP internas.
- EN: integration with chat/AI flows and internal MCP tooling.
- PT_BR: instalador NSIS, bootstrap online e versionamento por tag.
- EN: NSIS installer, online bootstrap flow and tag-driven versioning.

## Project Structure / Estrutura do Projeto

- `src/main.go`: GUI and service entrypoint / entrypoint de GUI e servico.
- `src/app`: main runtime domain / dominio principal do runtime.
- `src/internal`: internal services and integrations / servicos e integracoes internas.
- `src/frontend`: Wails front-end / interface Wails.
- `src/build/windows/installer`: NSIS installer sources / fontes do instalador NSIS.
- `DOCs`: technical and operational docs / documentacao tecnica e operacional.

## Local Build Requirements / Requisitos de Build Local

- Windows 10 or newer / Windows 10 ou superior.
- Go 1.23 or newer / Go 1.23 ou superior.
- NSIS (`makensis`) in `PATH`.
- Optional for GUI development / opcional para GUI: Wails CLI.

## Local Build / Build Local

```powershell
Set-Location .\src
go mod tidy
go test ./...
go build ./...
```

## Standard Installer Build / Build do Instalador Padrao

```powershell
.\build\scripts\build-install-installer.ps1 -ProjectRoot $PWD
```

Expected output / Saida esperada:

- `src/build/bin/discovery.exe`
- `src/build/bin/discovery-agent-install.exe`

Useful parameters / Parametros uteis:

```powershell
.\build\scripts\build-install-installer.ps1 `
    -ProjectRoot $PWD `
    -OutputName discovery-agent-acme.exe `
    -DefaultUrl api.example.com `
    -DefaultKey <token> `
    -DiscoveryEnabled 1 `
    -MinimalDefault
```

## Bootstrap Build / Build do Bootstrap

```powershell
.\build\scripts\build-bootstrap-installer.ps1 `
    -ProjectRoot $PWD `
    -PayloadUrl "https://cdn.example.com/discovery-agent-install.exe" `
    -PayloadSha256 "<optional-sha256>"
```

## GitHub Actions / Automacao no GitHub Actions

Workflows / Workflows:

- `.github/workflows/build-agent-installer.yml`
- `.github/workflows/build-agent-bootstrap.yml`
- `.github/workflows/release-agent-on-tag.yml`

How to run manually / Como executar manualmente:

1. Open GitHub Actions / Abra o GitHub Actions.
2. Select `Build Agent Installer` or `Build Agent Bootstrap Installer` / Selecione `Build Agent Installer` ou `Build Agent Bootstrap Installer`.
3. Use `Run workflow` and fill the inputs / Use `Run workflow` e preencha os parametros.
4. Download the generated artifact / Baixe o artefato gerado.

## Tag-Based Releases / Releases por Tag

Supported channels / Canais suportados:

- Stable: `v1.2.3`
- Beta: `v1.2.3-beta.1`
- LTS: `v1.2.3-lts.1`

Behavior / Comportamento:

- `v1.2.3`: stable GitHub release / release estavel.
- `v1.2.3-beta.1`: prerelease / prerelease.
- `v1.2.3-lts.1`: LTS release without `latest` / release LTS sem `latest`.

The workflow embeds the tag version into the binaries and publishes customer-facing assets automatically / O workflow embute a versao da tag no binario e publica os assets automaticamente.

## Network Operation Note / Observacao sobre Operacao em Rede

PT_BR: o instalador padrao registra e inicia o Windows Service do agent com discovery/P2P habilitado por padrao para operacao em rede local. Regras de firewall e politicas do ambiente ainda podem ser necessarias.

EN: the standard installer registers and starts the Windows Service with discovery/P2P enabled by default for local network operation. Firewall rules and environment policies may still be required.

## Documentation / Documentacao

- `DOCs/README.md`
- `DOCs/GUIA_INSTALADOR_OFFLINE_ONLINE_API.md`
- `ARCHITECTURE.md`
