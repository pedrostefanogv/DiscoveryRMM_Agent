# Plano: Instalador Offline e Online (NSIS)

> Status de consolidacao: historico.
>
> Este plano foi majoritariamente implementado (bootstrap installer, parametros ARG_BOOTSTRAP_INSTALL e ARG_PAYLOAD_URL, script build-bootstrap-installer.ps1).
>
> Fonte canonica atual do tema: DOCs/INSTALADOR_PAYLOAD_E_PARAMETROS.md

> Status original do documento: pendente de implementação
> Data de elaboração: 30/03/2026

## Objetivo

Criar dois artefatos de instalação a partir do mesmo `project.nsi`, controlados por flag de compile-time (`ONLINE_INSTALL`):
- **Offline**: embute o binário (comportamento atual)
- **Bootstrapper**: stub leve (~2–4 MB) que grava configuração local e baixa um **instalador completo de segunda etapa** a partir de uma URL definida na build.

---

## Fase 1 — Plugin `inetc` (pré-requisito)

1. Baixar `inetc.dll` (x86-unicode) em https://nsis.sourceforge.io/Inetc_plug-in
2. Adicionar em `build/windows/installer/plugins/x86-unicode/inetc.dll`
3. Adicionar `!addplugindir "${__FILEDIR__}\plugins"` no topo do `project.nsi`

> Motivo: `NSISdl` (embutido no NSIS) só suporta HTTP. `inetc` suporta HTTPS, exibe barra de progresso MUI2-compatível.

---

## Fase 2 — Modificar `build/windows/installer/project.nsi`

4. Adicionar defines condicionais no topo:
   ```nsis
   !ifdef ARG_ONLINE_INSTALL
     !define BUILD_ONLINE_INSTALL "1"
   !endif
   !ifdef ARG_DOWNLOAD_URL
     !define BUILD_DOWNLOAD_URL "${ARG_DOWNLOAD_URL}"
   !endif
   ```

5. No bloco `Section "Install"`, substituir `!insertmacro wails.files` por lógica condicional:
   - **Modo offline** (padrão): `!insertmacro wails.files` — sem alteração
   - **Modo bootstrapper**: baixa o instalador completo (`agent-install.exe` de segunda etapa), valida SHA256 opcional e executa em modo silencioso.

   ```nsis
   !ifdef BUILD_ONLINE_INSTALL
     inetc::get \
       /CAPTION "Baixando Discovery Agent" \
       /BANNER "Por favor aguarde o download..." \
       "${BUILD_DOWNLOAD_URL}" "$INSTDIR\${PRODUCT_EXECUTABLE}" \
       /END
     Pop $0
     StrCmp $0 "OK" +3
       MessageBox MB_OK|MB_ICONSTOP "Falha no download: $0$\nVerifique sua conexão e tente novamente."
       Quit
   !else
     !insertmacro wails.files
   !endif
   ```

6. WebView2: **manter embutido** nos dois modos (bootstrapper = ~1.7 MB, simplifica o online installer)

> Sem mudanças: wizard de configuração, `config.json`/`installer.json`, serviços registrados, uninstall.

---

## Fase 3 — Build Scripts

7. Ajustar `build/scripts/build-install-installer.ps1`
   - Compila `discovery.exe` via `go build`
   - Chama `makensis` no perfil full offline
   - Saída: `build/bin/discovery-amd64-installer-offline.exe`
   - Parâmetros: `-DefaultUrl`, `-DefaultKey`, `-Generic` (mesmos do script atual)

8. Criar `build/scripts/build-bootstrap-installer.ps1`
   - Compila `discovery.exe` via `go build`
   - Chama `makensis -DARG_BOOTSTRAP_INSTALL=1 -DARG_PAYLOAD_URL=<url>`
   - Saída: `build/bin/agent-install-online.exe`
   - Parâmetros obrigatórios: `-PayloadUrl`; opcionais: `-PayloadSha256`, `-DefaultUrl`, `-DefaultKey`

---

## Fase 4 — Hospedagem do Binário

9. Definir URL de hospedagem (em aberto — ver Decisões)
   - Formato recomendado: `https://host/discovery/v1.0.0/discovery-amd64.exe`

---

## Arquivos a modificar/criar

| Arquivo | Ação |
|---|---|
| `build/windows/installer/project.nsi` | Modificar — suporte a `BOOTSTRAP_INSTALL`, `PAYLOAD_URL`, `PAYLOAD_SHA256` |
| `build/windows/installer/plugins/x86-unicode/inetc.dll` | Adicionar ao repositório |
| `build/scripts/build-install-installer.ps1` | Ajustar para perfil full offline |
| `build/scripts/build-bootstrap-installer.ps1` | Criar |

---

## Verificação

1. **Offline**: instalar sem internet → serviços `DiscoveryAgent` e `DiscoveryAgentUI` ativos
2. **Bootstrapper**: instalar com internet → grava config + baixa instalador completo + serviços ativos
3. **Bootstrapper sem internet**: mensagem de erro limpa, sem crash
4. **Silent**: `agent-install-online.exe /S /URL=https://server /KEY=xyz /PAYLOAD_URL=https://cdn/agent-install.exe`
5. **Tamanho**: bootstrapper < 5 MB, full offline ≈ tamanho atual

---

## Decisões em aberto

1. **Verificação de integridade do download**
   - Opção A: SHA256 checksum definido no build, verificado após download (mais seguro)
   - Opção B: só HTTPS + code signing do `discovery.exe` hospedado (mais simples)
   - *Recomendação: começar com B, evoluir para A*

2. **URL de hospedagem**: GitHub Releases, S3/Cloudflare R2, ou servidor próprio?

3. **WebView2 no online installer**: manter embutido (recomendado) ou também baixar da internet?
