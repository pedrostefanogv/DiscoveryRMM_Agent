# Guia Operacional: Instalador Offline/Online + Integração com API

> Status de consolidacao: duplicado parcial.
>
> Fonte canonica atual do tema: DOCs/INSTALADOR_PAYLOAD_E_PARAMETROS.md
>
> Este arquivo foi reduzido para ponteiro. Detalhes operacionais e contratuais devem ser mantidos apenas na fonte canonica.

## Onde editar daqui para frente

1. Fluxo e parametros oficiais de instalador: `DOCs/INSTALADOR_PAYLOAD_E_PARAMETROS.md`
2. Mapa consolidado da documentacao: `DOCs/README.md`
3. Historico do plano de implantacao: `DOCs/HISTORICO/PLANO_INSTALADOR_OFFLINE_ONLINE.md`

## Atalhos rapidos

```powershell
# Build full offline
.\build\scripts\build-install-installer.ps1 -ProjectRoot c:\Projetos\Discovery

# Build bootstrapper
.\build\scripts\build-bootstrap-installer.ps1 -ProjectRoot c:\Projetos\Discovery -PayloadUrl "https://cdn/seu-canal/sua-versao/agent-install.exe"
```
