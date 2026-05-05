# Config do Agent

Este documento descreve os arquivos locais de configuração usados pelo agent Discovery e o significado de cada campo.

## Arquivos e precedência

1. `config.json`: arquivo canônico do agent. É a fonte usada no startup para bootstrap, credenciais, API, NATS, self-update e P2P.
2. `installer.json`: override opcional aplicado sobre `config.json` para alguns campos de instalação/bootstrap.
3. `debug_config.json`: persistência do módulo de debug. Pode refletir o estado de runtime, mas o startup operacional do agent deve ser tratado a partir de `config.json`.

## Caminhos procurados no Windows

1. `C:\ProgramData\Discovery\config.json`
2. `C:\Users\<usuario>\AppData\Local\Discovery\config.json`
3. Diretório do executável
4. `C:\Users\<usuario>\.discovery\config.json`
5. Diretório atual

## Exemplo mínimo

```json
{
  "serverUrl": "https://discovery.exemplo.local",
  "deployToken": "SEU_DEPLOY_TOKEN",
  "allowInsecureTls": true
}
```

## Campos de `config.json`

| Campo | Tipo | Obrigatório | Descrição |
|---|---|---:|---|
| `serverUrl` | string | não | URL base legada/canônica do servidor. Se `apiScheme`/`apiServer` não vierem preenchidos, o agent tenta derivá-los daqui. |
| `deployToken` | string | não | Token de bootstrap usado em `/api/agent-install/register`. Depois que o agent recebe `authToken` e `agentId`, esse campo tende a ser removido do arquivo. |
| `apiKey` | string | não | Alias legado de leitura para `deployToken`. Não deve ser usado em novas instalações. |
| `autoProvisioning` | bool | não | Habilita a participação local no fluxo de **zero-touch auto-provisioning** via P2P. Quando `false`, o agente não inicia o loop de onboarding mesmo estando não configurado. Quando ausente, o agente segue a feature flag do servidor. **Substitui o legado `discoveryEnabled`**, que ainda é aceito em leitura para retrocompat. |
| `apiScheme` | string | não | Esquema da API. Valores esperados: `http` ou `https`. Em ambiente remoto, o padrão operacional é `https`. |
| `apiServer` | string | não | Host da API sem path, por exemplo `192-168-1-131.nip.io` ou `api.exemplo.local:8443`. |
| `authToken` | string | não | Token definitivo do agent após bootstrap. É usado nas chamadas autenticadas para API e sync. |
| `agentId` | string | não | Identificador definitivo do agent após bootstrap. Também é usado nos fluxos autenticados de API e NATS. |
| `natsServer` | string | não | Endpoint NATS canônico. Pode ser `nats://host:4222`, `ws://...` ou `wss://...`, respeitando as políticas de segurança do runtime. |
| `natsWsServer` | string | não | Endpoint alternativo dedicado para NATS sobre WebSocket, normalmente `wss://...`. |
| `allowInsecureTls` | bool | não | Quando `true`, o agent aceita certificado autoassinado ou cadeia não confiável do servidor. Afeta bootstrap, chamadas HTTP autenticadas, automação e NATS WSS. Deve ser usado apenas em laboratório ou ambientes controlados. |
| `agentUpdate` | objeto | não | Política de self-update do agent. Campos descritos abaixo. |
| `p2p` | objeto | não | Configuração local do subsistema P2P. Campos descritos abaixo. |
| `meshCentralInstalled` | bool | não | Estado persistido do bootstrap do MeshCentral. Campo interno do agent; normalmente não precisa edição manual. |

## Campos de `agentUpdate`

| Campo | Tipo | Obrigatório | Descrição |
|---|---|---:|---|
| `enabled` | bool | não | Liga/desliga o mecanismo de self-update. |
| `checkOnStartup` | bool | não | Executa verificação de update na inicialização. |
| `checkPeriodically` | bool | não | Executa verificações periódicas enquanto o agent está em execução. |
| `checkOnSyncManifest` | bool | não | Permite que o sync-manifest dispare verificação de update. |
| `checkEveryHours` | int | não | Intervalo de verificação periódica, em horas. |
| `preferredArtifactType` | string | não | Tipo preferido do artefato de update. O valor mais comum no projeto é `Installer`. |
| `requireSignatureValidation` | bool | não | Exige validação de assinatura do artefato antes da instalação. |

## Campos de `p2p`

| Campo | Tipo | Obrigatório | Descrição |
|---|---|---:|---|
| `enabled` | bool | não | Habilita o subsistema P2P. |
| `discoveryMode` | string | não | Estratégia de descoberta de peers. |
| `p2pMode` | string | não | Modo operacional do P2P. Valor conhecido no código: `libp2p_only`. |
| `tempTtlHours` | int | não | TTL, em horas, para artefatos temporários locais. |
| `seedPercent` | int | não | Percentual alvo de seeds em uma distribuição. |
| `minSeeds` | int | não | Quantidade mínima de seeds desejada. |
| `httpListenPortRangeStart` | int | não | Início do range de portas HTTP local do P2P. |
| `httpListenPortRangeEnd` | int | não | Fim do range de portas HTTP local do P2P. |
| `authTokenRotationMinutes` | int | não | Janela de rotação do token temporário usado no acesso aos artefatos P2P. |
| `sharedSecret` | string | não | Segredo compartilhado do P2P quando aplicável. |
| `chunkSizeBytes` | int64 | não | Tamanho dos chunks de download P2P. |
| `maxBandwidthBytesPerSec` | int64 | não | Limite de banda do P2P em bytes por segundo. |
| `bootstrapConfig` | objeto | não | Configuração de bootstrap P2P, descrita abaixo. |

## Campos de `p2p.bootstrapConfig`

| Campo | Tipo | Obrigatório | Descrição |
|---|---|---:|---|
| `bootstrapPeers` | array de string | não | Lista de peers iniciais para descoberta. |
| `preferLan` | bool | não | Dá preferência a peers LAN. |
| `cloudBootstrapEnabled` | bool | não | Permite bootstrap complementar via nuvem. |

## Campos suportados em `installer.json`

O arquivo `installer.json` não substitui todo o `config.json`. Hoje ele é tratado como override para estes campos:

1. `serverUrl`
2. `deployToken` ou `apiKey`
3. `apiScheme`
4. `apiServer`
5. `autoProvisioning` (legado: `discoveryEnabled`)
6. `natsServer`
7. `natsWsServer`
8. `allowInsecureTls`

## Certificado autoassinado

Para API com certificado autoassinado, configure:

```json
{
  "apiScheme": "https",
  "apiServer": "192-168-1-131.nip.io",
  "deployToken": "SEU_DEPLOY_TOKEN",
  "allowInsecureTls": true
}
```

Com `allowInsecureTls=true`, o agent passa a aceitar a conexão TLS mesmo quando o certificado não está ancorado na trust store do Windows. Isso resolve o cenário de laboratório com CA própria ou certificado self-signed, mas reduz a proteção contra MITM. Em produção, o recomendado continua sendo instalar a CA raiz correta no sistema e manter `allowInsecureTls=false`.

O instalador Windows também aceita o parâmetro silencioso `/ALLOW_INSECURE_TLS=`. Quando ele recebe um valor verdadeiro (`1`, `true`, `yes` ou `on`), o instalador persiste `"allowInsecureTls": true` no `config.json` durante a instalação. Se o parâmetro não for informado, o instalador não cria esse campo por padrão.

O instalador também aceita `/AUTO_PROVISIONING=0|1` (alias legado: `/DISCOVERY=0|1`) para definir o valor inicial de `autoProvisioning` no `config.json`. O default do build é `1` (auto-provisioning habilitado).

## Observações operacionais

1. Se `authToken` e `agentId` estiverem vazios, o agent tenta bootstrap usando `deployToken`.
2. Se `apiScheme`/`apiServer` estiverem vazios, o agent tenta derivá-los de `serverUrl`.
3. `authToken` e `agentId` normalmente são mantidos pelo próprio agent; evitar edição manual quando o bootstrap já foi concluído.
4. A variável de ambiente `DISCOVERY_ALLOW_INSECURE_TLS=1` continua existindo como fallback de laboratório, mas o caminho preferido agora é `allowInsecureTls` no `config.json`.