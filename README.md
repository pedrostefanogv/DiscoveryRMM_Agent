<div align="center">

# Discovery Agent

**PT_BR:** Agente Windows para inventário, automação, distribuição P2P e operação local/remota.
**EN:** Windows agent for inventory, automation, P2P distribution and local/remote operation.

[![Stable](https://img.shields.io/github/v/release/pedrostefanogv/DiscoveryRMM_Agent?style=flat-square&logo=github&label=stable&color=2ea44f)](https://github.com/pedrostefanogv/DiscoveryRMM_Agent/releases/latest)
[![Beta](https://img.shields.io/github/v/release/pedrostefanogv/DiscoveryRMM_Agent?include_prereleases&filter=*-beta*&style=flat-square&logo=github&label=beta&color=0075ca)](https://github.com/pedrostefanogv/DiscoveryRMM_Agent/releases?q=beta&expanded=true)
[![LTS](https://img.shields.io/github/v/release/pedrostefanogv/DiscoveryRMM_Agent?include_prereleases&filter=*-lts*&style=flat-square&logo=github&label=lts&color=e4a600)](https://github.com/pedrostefanogv/DiscoveryRMM_Agent/releases?q=lts&expanded=true)
[![Downloads](https://img.shields.io/github/downloads/pedrostefanogv/DiscoveryRMM_Agent/total?style=flat-square&logo=github)](https://github.com/pedrostefanogv/DiscoveryRMM_Agent/releases)
[![Release Workflow](https://img.shields.io/github/actions/workflow/status/pedrostefanogv/DiscoveryRMM_Agent/release-agent-on-tag.yml?style=flat-square&logo=githubactions&label=release%20build)](https://github.com/pedrostefanogv/DiscoveryRMM_Agent/actions/workflows/release-agent-on-tag.yml)
[![License](https://img.shields.io/github/license/pedrostefanogv/DiscoveryRMM_Agent?style=flat-square)](LICENSE)

[![Platform](https://img.shields.io/badge/platform-Windows%2010%2B-0078D6?style=flat-square&logo=windows)](https://github.com/pedrostefanogv/DiscoveryRMM_Agent/releases/latest)
[![Go](https://img.shields.io/badge/go-1.23%2B-00ADD8?style=flat-square&logo=go)](https://go.dev/)
[![Wails](https://img.shields.io/badge/UI-Wails-DF0000?style=flat-square)](https://wails.io/)
[![Installer](https://img.shields.io/badge/installer-NSIS-2A6A9C?style=flat-square)](https://nsis.sourceforge.io/)

</div>

---

## AI / VibeCode Notice

> **PT_BR:** este repositório contém partes desenvolvidas com apoio de fluxos assistidos por IA (VibeCode e similares). Revise, valide e teste tudo antes de uso produtivo. Se sua política interna restringe código assistido por IA, não utilize sem aprovação formal.
>
> **EN:** this repository contains parts developed with AI-assisted workflows (VibeCode and similar). Review, validate and test everything before production use. If your internal policy restricts AI-assisted code, do not use without formal approval.

---

## ✨ Features / Recursos

| | PT_BR | EN |
|---|---|---|
| 🖥️ | Modo GUI desktop ou serviço Windows headless | Desktop GUI or headless Windows Service |
| 📦 | Inventário de hardware e software (osquery + PowerShell) | Hardware/software inventory (osquery + PowerShell) |
| ⚙️ | Automação operacional e execução de tarefas | Operational automation and task execution |
| 🔗 | Distribuição P2P com descoberta local | P2P distribution with local discovery |
| 🤖 | Integração com chat/IA e ferramentas MCP | Chat/AI integration and MCP tooling |
| 📥 | Instalador NSIS, bootstrap online, versionamento por tag | NSIS installer, online bootstrap, tag versioning |

---

## 📥 Download / Instalação

**PT_BR:** baixe o instalador mais recente na página de releases.
**EN:** grab the latest installer from the releases page.

| Canal / Channel | Tag | Link |
|---|---|---|
| 🟢 Stable | `vX.Y.Z` | [Latest release](https://github.com/pedrostefanogv/DiscoveryRMM_Agent/releases/latest) |
| 🔵 Beta | `vX.Y.Z-beta.N` | [Pre-releases](https://github.com/pedrostefanogv/DiscoveryRMM_Agent/releases?q=prerelease%3Atrue) |
| 🟡 LTS | `vX.Y.Z-lts.N` | [All releases](https://github.com/pedrostefanogv/DiscoveryRMM_Agent/releases) |

> **PT_BR:** o instalador padrão registra o serviço Windows com discovery/P2P habilitado por padrão. Regras de firewall podem ser necessárias.
> **EN:** the default installer registers the Windows Service with discovery/P2P enabled by default. Firewall rules may be required.

---

## 🔗 Repositórios Relacionados / Related Repositories

- **Agent (este / this):** https://github.com/pedrostefanogv/DiscoveryRMM_Agent
- **API Server:** https://github.com/pedrostefanogv/DiscoveryRMM_API

---

## 🌿 Branching Model

```
feature/* ──► dev ──► beta ──► release ──► lts
                                  │
                                  └─► tag vX.Y.Z ──► GitHub Release
```

| Branch | Propósito / Purpose |
|---|---|
| `dev` | Integração de features e bugfixes / Feature & bugfix integration |
| `beta` | Validação pré-release / Pre-release validation |
| `release` | **Default** — produção estável / production stable |
| `lts` | Long-term support |

Detalhes em [BRANCHING.md](BRANCHING.md).

---

## � Modo Debug / Debug Mode

**PT_BR:** Para abrir a interface do agente em modo debug:
1. Feche o agente pelo ícone na bandeja do sistema (clique com botão direito → **Sair / Exit**).
2. Abra o agente novamente **segurando `Shift`** ao clicar no ícone/atalho.

> O modo debug exibe logs detalhados na interface e habilita painéis de diagnóstico internos.

**EN:** To open the agent UI in debug mode:
1. Close the agent from the system tray icon (right-click → **Exit**).
2. Reopen the agent **while holding `Shift`** when clicking the icon/shortcut.

> Debug mode shows detailed logs in the UI and enables internal diagnostics panels.

---

## �📚 Documentation / Documentação

- [ARCHITECTURE.md](ARCHITECTURE.md) — visão arquitetural / architecture overview
- [SECURITY.md](SECURITY.md) — política de segurança / security policy
- [BRANCHING.md](BRANCHING.md) — fluxo de branches / branch workflow
- [DOCs/](DOCs/) — guias técnicos e operacionais / technical and operational guides

---

## 🛠️ Build (Contribuidores / Contributors)

**PT_BR:** este projeto usa GitHub Actions para build oficial. Para desenvolvimento local consulte [DOCs/README.md](DOCs/README.md).
**EN:** official builds run on GitHub Actions. For local development see [DOCs/README.md](DOCs/README.md).

Requisitos mínimos / Minimum requirements: Windows 10+, Go 1.23+, NSIS no `PATH`.

---

<div align="center">

Feito com 💙 por [@pedrostefanogv](https://github.com/pedrostefanogv)

</div>
