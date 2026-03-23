# Instalador: Payload e Parametros (Fonte Unica)

Este documento consolida os dados de payload e os parametros aceitos no fluxo do instalador.

Escopo coberto:
- API de geracao de instalador (payload)
- API de bootstrap pos-instalacao (`POST /api/agent-install/register`)
- Parametros de build do instalador (Wails + NSIS)
- Parametros de execucao do instalador (.exe)
- Comportamento de instalacao all-users (Windows Service + Task Scheduler)

## 0. Fluxo pos-instalacao (URL + KEY)

Fluxo esperado apos instalar:
1. Instalador grava `C:\ProgramData\Discovery\config.json`.
2. No startup, o app le esse arquivo.
3. Se necessario, o app usa `apiKey`/deploy token e chama `POST /api/agent-install/register` no servidor informado.
4. A API retorna `token` + `agentId`.
5. O app persiste esses dados no proprio `config.json` de producao (`authToken` e `agentId`) para as proximas inicializacoes.
6. A `apiKey` e removida do arquivo apos o primeiro registro bem-sucedido.

Caminho padrao do arquivo de producao no Windows:

```text
C:\ProgramData\Discovery\config.json
```

Observacao:
- O fluxo legado de auto-registro por `deployToken` foi removido.
- O caminho oficial de bootstrap e `POST /api/agent-install/register` com `Authorization: Bearer <deploy_token>`.
- O instalador remove um `config.json` legado no diretório de instalação para evitar conflito de origem.
- Se o instalador nao conseguir criar/gravar `C:\ProgramData\Discovery\config.json`, a instalacao e interrompida com erro.
- O arquivo pode conter campos canônicos e legados por compatibilidade (`server_url`/`auth_token` e `serverUrl`/`apiKey`).

### Cache temporario P2P (Windows)

Para cenarios com multiplos usuarios no mesmo host, o cache P2P usa diretório compartilhado no Windows:

```text
C:\Windows\Temp\Discovery\P2P_Temp
```

Esse caminho e usado para publicar, baixar e limpar artifacts temporarios do P2P.

Payload enviado para `POST /api/agent-install/register`:

```json
{
  "hostname": "DESKTOP-ABC123",
  "displayName": "DESKTOP-ABC123",
  "operatingSystem": "Windows",
  "osVersion": "Windows_NT",
  "agentVersion": "1.0.0",
  "macAddress": "AA:BB:CC:DD:EE:FF"
}
```

Header obrigatorio:

```text
Authorization: Bearer <deploy_token>
```

Resposta esperada (qualquer envelope com esses campos):

```json
{
  "clientId": "guid...",
  "siteId": "guid...",
  "token": "agent_token_xpto",
  "agentId": "3fa85f64-5717-4562-b3fc-2c963f66afa6",
  "tokenId": "guid...",
  "expiresAt": null
}
```

O parser tambem aceita os campos em envelopes como `data`/`result` e nomes alternativos de token (`authToken`, `accessToken`).

## 1. Payload da API de geracao

Endpoint de referencia:
- `POST /api/installers/generate`

Payload minimo:

```json
{
  "clientId": "acme-001",
  "serverHost": "api.acme.meduza.com",
  "apiKey": "key_xpto",
  "discoveryEnabled": true,
  "silentDefault": false,
  "minimalDefault": false,
  "expiresInMinutes": 30
}
```

Campos do payload:

| Campo | Tipo | Obrigatorio | Descricao |
|---|---|---|---|
| `clientId` | string | sim | Identificador do cliente/tenant para rastreio e nomeacao de artefato. |
| `serverHost` | string | sim | Host do backend sem protocolo, ex.: `api.example.com`. |
| `apiKey` | string | sim | Chave de autenticacao do agente. |
| `discoveryEnabled` | boolean | sim | Define estado inicial do discovery no agente (`true`/`false`). |
| `silentDefault` | boolean | sim | Define default para instalacao silenciosa no template dinamico. |
| `minimalDefault` | boolean | sim | Define default para modo minimo no template dinamico. |
| `expiresInMinutes` | number | sim | Tempo de expiracao da URL de download do instalador. |

Resposta esperada:

```json
{
  "requestId": "req_20260306_001",
  "downloadUrl": "https://download.meduza.com/i/req_20260306_001.exe",
  "expiresAt": "2026-03-06T15:00:00Z"
}
```

Endpoint de status:
- `GET /api/installers/status/{requestId}`
- Status previstos: `queued`, `running`, `done`, `failed`

## 2. Parametros de build do instalador

### 2.1 Gerar contexto NSIS via Wails

Comando de referencia:

```powershell
wails build --target windows/amd64 --nsis
```

Uso: gera os artefatos de packaging e popula macros usadas pelo NSIS (`wails_tools.nsh`).

### 2.2 Compilar instalador com makensis

Parametros aceitos pelo script NSIS atual:

- `-DARG_WAILS_AMD64_BINARY=<caminho>`
- `-DARG_WAILS_ARM64_BINARY=<caminho>`
- `-DARG_DEFAULT_URL=<host-ou-url>` (opcional)
- `-DARG_DEFAULT_KEY=<apiKey>` (opcional)
- `-DARG_DEFAULT_DISCOVERY=0|1` (opcional, default `1`)
- `-DARG_DEFAULT_MINIMAL=0|1` (opcional, default `0`)

Regra de prioridade:
- Defaults de build sao aplicados primeiro.
- Parametros passados na execucao do instalador sobrescrevem os defaults de build.

Exemplos:

```powershell
# Somente AMD64
makensis -DARG_WAILS_AMD64_BINARY=..\..\bin\app.exe

# Somente ARM64
makensis -DARG_WAILS_ARM64_BINARY=..\..\bin\app.exe

# Multi-arquitetura
makensis -DARG_WAILS_AMD64_BINARY=..\..\bin\app-amd64.exe -DARG_WAILS_ARM64_BINARY=..\..\bin\app-arm64.exe

# Build com defaults embutidos (URL/KEY/DISCOVERY/MINIMAL)
makensis -DARG_WAILS_AMD64_BINARY=..\..\bin\discovery.exe -DARG_DEFAULT_URL=api.acme.meduza.com -DARG_DEFAULT_KEY=key_xpto -DARG_DEFAULT_DISCOVERY=1 -DARG_DEFAULT_MINIMAL=1
```

Saida padrao definida no NSIS:
- `build/bin/<project>-<ARCH>-installer.exe`

## 3. Parametros de execucao do instalador (.exe)

O instalador aceita parametros de linha de comando na inicializacao.

| Parametro | Tipo | Exemplo | Efeito |
|---|---|---|---|
| `/URL=` | string | `/URL="api.alt.example.com"` | Preenche URL do servidor no wizard/config. |
| `/KEY=` | string | `/KEY="abc123"` | Preenche chave de API no wizard/config. |
| `/DISCOVERY=` | `0` ou `1` | `/DISCOVERY=0` | Define discovery desabilitado/habilitado. |
| `/MINIMAL` | flag | `/MINIMAL` | Ativa modo minimo; pode pular wizard dependendo dos dados informados. |

Observacoes de comportamento:
- Se `/URL` e `/KEY` forem informados, o wizard pode ser pulado.
- Se `/MINIMAL` for usado com URL/KEY, o instalador entra em fluxo reduzido.
- Instalacao silenciosa NSIS padrao continua disponivel via `/S` (comportamento NSIS).
- Em caso de conflito, CLI de instalacao prevalece sobre os defaults embutidos no build.

## 4. Comportamento all-users no Windows (implementacao atual)

Durante a instalacao, o NSIS executa estas acoes:

1. Registra o Windows Service `DiscoveryAgent` com inicio automatico (`start= auto`) e argumento `--service`.
2. Configura politica de recuperacao do service para reiniciar automaticamente em falhas.
3. Registra task agendada `DiscoveryAgentUI` com trigger `At log on of any user`.
4. A task inicia a UI com delay de 30 segundos e argumentos:

```text
--startup-minimized --startup-source=task-scheduler
```

5. Remove atalho legado de startup (`$SMSTARTUP`) durante migracao.

No uninstall:

1. Remove service `DiscoveryAgent`.
2. Remove task `DiscoveryAgentUI`.
3. Mantem limpeza de atalho legado de startup.

Comandos uteis para verificacao:

```powershell
sc query DiscoveryAgent
schtasks /query /tn "DiscoveryAgentUI"
```

## 5. Exemplo completo (override manual)

```powershell
discovery-acme-001.exe /URL="api.alt.example.com" /KEY="outra-key" /DISCOVERY=0 /MINIMAL
```

## 6. Placeholders do template dinamico (quando aplicavel)

No fluxo de geracao dinamica de `project.nsi.template`, os placeholders documentados sao:

- `{{INFO_PROJECTNAME}}`
- `{{INFO_COMPANYNAME}}`
- `{{INFO_PRODUCTNAME}}`
- `{{PRODUCT_EXECUTABLE}}`
- `{{UNINST_KEY_NAME}}`
- `{{SERVER_HOST}}`
- `{{API_KEY}}`
- `{{DISCOVERY_ENABLED}}`
- `{{SILENT_DEFAULT}}`
- `{{MINIMAL_DEFAULT}}`
- `{{CLIENT_ID}}`

## 7. Fontes no repositorio

- `build/windows/installer/project.nsi`
- `DOCs/OPCAO1_TEMPLATE_NSIS.md`
- `wails.json`
