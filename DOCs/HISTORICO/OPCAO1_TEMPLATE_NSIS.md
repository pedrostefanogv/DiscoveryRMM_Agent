# Opcao 01: Instalador Dinamico com Template NSIS (Servidor C#)

> Status de consolidacao: historico conceitual.
>
> Este documento descreve uma alternativa arquitetural de geracao dinamica e nao deve ser tratado como fluxo padrao atual.
>
> Fonte canonica atual do tema de instalador: DOCs/INSTALADOR_PAYLOAD_E_PARAMETROS.md

## Objetivo
Implementar geracao sob demanda de instaladores do Discovery, ja pre-configurados por cliente/dispositivo (URL, API Key, flags de comportamento), sem recompilar o binario Go/Wails a cada solicitacao.

## Resumo da Estrategia
A estrategia usa um binario base ja compilado do agente (`discovery.exe`) e um template NSIS (`project.nsi.template`).

Fluxo:
1. API C# recebe requisicao para gerar instalador.
2. Servidor busca dados do cliente (URL, chave, perfil de instalacao).
3. Servidor substitui placeholders no template NSIS.
4. Servidor executa `makensis` e gera um instalador unico.
5. Instalador final e disponibilizado para download.

## Quando usar esta opcao
Use esta opcao quando precisar:
- Gerar instalador unico por cliente rapidamente.
- Evitar custo de recompilar Go/Wails em toda solicitacao.
- Controlar parametros por tenant/organizacao/dispositivo.

Nao use esta opcao se:
- For obrigatorio embutir segredo no codigo Go (neste caso, build dinamico com `ldflags` pode ser melhor).

## Arquitetura Recomendada

```text
Cliente/Admin Portal
    -> API C# (Generate Installer)
        -> Banco (dados do cliente/licenca)
        -> Pasta de templates (NSIS + binario base)
        -> makensis (compilacao)
        -> Storage (arquivo final)
    -> URL assinada para download
```

## Estrutura de Pastas (Servidor)

```text
templates/
  discovery.exe
  project.nsi.template
  icon.ico
  LICENSE.txt (opcional)

generated/
  {requestId}/
    project.nsi
    discovery.exe
    discovery-{clientId}.exe

logs/
  installer-jobs.log
```

## Contrato de API (Minimo)

### POST /api/installers/generate
Request:

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

Response:

```json
{
  "requestId": "req_20260306_001",
  "downloadUrl": "https://download.meduza.com/i/req_20260306_001.exe",
  "expiresAt": "2026-03-06T15:00:00Z"
}
```

### GET /api/installers/status/{requestId}
- Retorna status: queued, running, done, failed.

## Placeholders no Template NSIS
No `project.nsi.template`, manter placeholders claros:

```text
{{INFO_PROJECTNAME}}
{{INFO_COMPANYNAME}}
{{INFO_PRODUCTNAME}}
{{PRODUCT_EXECUTABLE}}
{{UNINST_KEY_NAME}}

{{SERVER_HOST}}
{{API_KEY}}
{{DISCOVERY_ENABLED}}
{{SILENT_DEFAULT}}
{{MINIMAL_DEFAULT}}
{{CLIENT_ID}}
```

## Exemplo de Template NSIS (trecho)

```nsis
!define INFO_PROJECTNAME    "discovery"
!define INFO_COMPANYNAME    "Meduza"
!define INFO_PRODUCTNAME    "Discovery"
!define PRODUCT_EXECUTABLE  "discovery.exe"
!define UNINST_KEY_NAME     "Meduza.Discovery"

!define SERVER_HOST         "{{SERVER_HOST}}"
!define API_KEY             "{{API_KEY}}"
!define CLIENT_ID           "{{CLIENT_ID}}"
!define DISCOVERY_ENABLED   "{{DISCOVERY_ENABLED}}"
!define SILENT_DEFAULT      "{{SILENT_DEFAULT}}"
!define MINIMAL_DEFAULT     "{{MINIMAL_DEFAULT}}"

Function .onInit
  ${If} "${SILENT_DEFAULT}" == "true"
    SetSilent silent
  ${EndIf}
FunctionEnd

Function SaveAgentConfig
  FileOpen $0 "$INSTDIR\\config.json" w
  FileWrite $0 "{$\\r$\\n"
  FileWrite $0 '  "clientId": "${CLIENT_ID}",$\\r$\\n'
  FileWrite $0 '  "serverHost": "${SERVER_HOST}",$\\r$\\n'
  FileWrite $0 '  "apiKey": "${API_KEY}",$\\r$\\n'
  FileWrite $0 '  "discoveryEnabled": ${DISCOVERY_ENABLED}$\\r$\\n'
  FileWrite $0 "}$\\r$\\n"
  FileClose $0
FunctionEnd
```

Observacao: no seu caso, `serverHost` deve ser host sem protocolo (ex.: `api.example.com`). O app pode montar `https://` em runtime.

## Servico C# (Fluxo de Geracao)

Passos no servico:
1. Validar payload e autorizacao da chamada.
2. Buscar/validar cliente e licenca.
3. Criar pasta temporaria unica (`generated/{requestId}`).
4. Copiar `discovery.exe` base e assets necessarios.
5. Ler template NSIS e substituir placeholders.
6. Gravar `project.nsi` final.
7. Executar `makensis`.
8. Validar que o arquivo final existe e tem tamanho esperado.
9. Subir em storage (S3/Azure Blob/local) e emitir URL assinada.
10. Agendar limpeza de arquivos temporarios.

## Exemplo C# (pseudo-codigo enxuto)

```csharp
public async Task<InstallerResult> GenerateAsync(GenerateInstallerRequest req)
{
    Validate(req);

    var requestId = $"req_{DateTime.UtcNow:yyyyMMdd_HHmmss}_{Guid.NewGuid():N}";
    var workDir = Path.Combine(_generatedRoot, requestId);
    Directory.CreateDirectory(workDir);

    File.Copy(Path.Combine(_templatesRoot, "discovery.exe"),
              Path.Combine(workDir, "discovery.exe"), true);

    var template = await File.ReadAllTextAsync(Path.Combine(_templatesRoot, "project.nsi.template"));

    var nsi = template
        .Replace("{{SERVER_HOST}}", EscapeNsis(req.ServerHost))
        .Replace("{{API_KEY}}", EscapeNsis(req.ApiKey))
        .Replace("{{CLIENT_ID}}", EscapeNsis(req.ClientId))
        .Replace("{{DISCOVERY_ENABLED}}", req.DiscoveryEnabled ? "true" : "false")
        .Replace("{{SILENT_DEFAULT}}", req.SilentDefault ? "true" : "false")
        .Replace("{{MINIMAL_DEFAULT}}", req.MinimalDefault ? "true" : "false");

    var nsiPath = Path.Combine(workDir, "project.nsi");
    await File.WriteAllTextAsync(nsiPath, nsi);

    var outExe = Path.Combine(workDir, $"discovery-{req.ClientId}.exe");
    await RunMakensisAsync(nsiPath, outExe, workDir);

    var url = await _artifactStore.PublishAsync(outExe, req.ExpiresInMinutes);
    return new InstallerResult(requestId, url);
}
```

## Seguranca (Critico)

1. Nunca confiar em input bruto para placeholders NSIS.
2. Escapar caracteres especiais antes de escrever no template.
3. Evitar logar API Key em texto puro.
4. Preferir URL assinada com expiração curta para download.
5. Limitar taxa de geracao por tenant/IP.
6. Registrar auditoria (quem gerou, quando, para qual cliente).
7. Se possivel, criptografar `apiKey` em repouso no `config.json` e descriptografar no agente.

## Operacao e Escalabilidade

- Fila de jobs (ex.: Hangfire/Quartz/RabbitMQ) para picos.
- Worker dedicado para compilacao NSIS.
- Cache de instaladores por hash de configuracao para evitar recompilar igual.

Hash sugerido:
- `SHA256(serverHost + clientId + discoveryEnabled + silentDefault + minimalDefault + versaoAgente)`

## Compatibilidade com os modos ja definidos

Com a implementacao atual do seu NSIS:
- Wizard completo por padrao.
- Modo minimo com `/MINIMAL`.
- Modo silencioso com `/S`.
- Parametros em CLI podem sobrescrever default embutido.

Exemplo final para override manual:

```powershell
discovery-acme-001.exe /URL="api.alt.example.com" /KEY="outra-key" /DISCOVERY=0 /MINIMAL
```

## Plano de Implementacao (pratico)

1. Congelar um binario base aprovado (`discovery.exe`).
2. Criar `project.nsi.template` com placeholders.
3. Implementar endpoint `POST /api/installers/generate`.
4. Implementar servico de geracao + execucao do `makensis`.
5. Publicar artefato em storage com URL assinada.
6. Adicionar auditoria, rate-limit e limpeza automatica.
7. Testar matriz:
   - sem parametros
   - com parametros CLI
   - silent default true/false
   - minimal default true/false

## Riscos e Mitigacoes

- Risco: vazamento de chave em logs.
  Mitigacao: mascarar segredo em logs e traces.

- Risco: injetar conteudo invalido no NSIS.
  Mitigacao: escape estrito e whitelist de caracteres para host/key.

- Risco: gerar muitos instaladores e crescer disco.
  Mitigacao: TTL + rotina de cleanup.

## Resultado esperado
Ao final, o servidor C# consegue entregar instaladores personalizados por cliente em segundos, sem recompilar o agente Go/Wails, mantendo compatibilidade com os parametros CLI e com os modos de instalacao do NSIS.
