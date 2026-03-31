# Plano de Build Modular MCP (WASM)

## Objetivo
Permitir que o Agent Discovery baixe e ative modulos MCP por cliente sem atualizar o aplicativo inteiro.

Decisoes adotadas:
- Isolamento: sandbox WASM no mesmo processo.
- Distribuicao: P2P como primario, com fallback HTTPS/TLS.
- Politica de seguranca: estrita (bloquear modulo invalido e registrar auditoria).

## Visao Geral
Em vez de DLL, cada modulo MCP e entregue como pacote de artefatos:
1. Arquivo `.wasm`
2. Manifesto JSON com metadados e permissoes
3. Assinatura digital do manifesto/pacote
4. Hash SHA-256 do modulo

O executavel principal continua monolitico e assinado. Somente os modulos MCP variam por cliente.

## Estrutura de Pastas (Endpoint)
Recomendacao: armazenar em ProgramData (nao na pasta do exe).

Exemplo:

```text
C:\ProgramData\Discovery\mcp-modules\
  staging\
  store\
    inventory\
      1.2.0\
        module.wasm
        manifest.json
        signature.sig
  active\
    inventory.json
  logs\
    mcp-modules-audit.log
```

Descricao:
- `staging`: download temporario antes de validacao.
- `store`: artefatos validados por modulo/versao.
- `active`: ponteiro para a versao ativa por modulo.
- `logs`: trilha de auditoria local.

## Contrato de Manifesto (Exemplo)
```json
{
  "moduleName": "inventory",
  "version": "1.2.0",
  "entrypoint": "register_tools",
  "minAgentVersion": "1.8.0",
  "target": "wasm32-wasi",
  "sha256": "<HASH_HEX>",
  "signatureAlgorithm": "ed25519",
  "signatureRef": "signature.sig",
  "capabilities": [
    "tool:inventory.read",
    "tool:inventory.export"
  ],
  "toolIds": [
    "inventory.get",
    "inventory.export_markdown"
  ],
  "compat": {
    "apiVersion": "mcp-module-v1"
  }
}
```

## Pipeline de Build do Modulo WASM
1. Compilar modulo para alvo WASI (`wasm32-wasi`).
2. Executar testes do modulo.
3. Gerar `manifest.json`.
4. Calcular SHA-256 do `module.wasm`.
5. Assinar manifesto/pacote com chave privada de release.
6. Publicar artefatos no repositorio central.
7. (Opcional) Semear artefatos no P2P para distribuicao local.

## Fluxo de Download e Ativacao no Agent
1. Agent recebe policy por cliente com lista de modulos permitidos e versoes alvo.
2. Para cada modulo:
   - tenta download via P2P
   - se falhar, fallback HTTPS/TLS
3. Salva em `staging`.
4. Valida:
   - assinatura
   - hash SHA-256
   - compatibilidade (`minAgentVersion`, `apiVersion`)
   - permissao por policy do cliente
5. Se valido: move para `store` e atualiza ponteiro em `active`.
6. Carrega modulo no runtime WASM com limites de tempo/memoria.
7. Registra auditoria local e envia evento ao servidor.

## Politica Estrita de Seguranca
Se qualquer validacao falhar:
- Bloquear carga do modulo.
- Manter versao anterior ativa.
- Registrar evento de auditoria com motivo.
- Notificar servidor de controle.

Eventos minimos de auditoria:
- `module_download_started`
- `module_download_failed`
- `module_validation_failed`
- `module_loaded`
- `module_blocked`
- `module_rollback`

## Rollback
Cenarios:
1. Falha de validacao no startup.
2. Timeout/panic em execucao do modulo.
3. Regressao funcional detectada por health checks.

Acoes:
- Desativar versao atual.
- Reativar ultima versao estavel do mesmo modulo.
- Se nao houver fallback, desabilitar modulo e manter core funcionando.

## Integracao com Componentes Existentes
Mapeamento esperado no projeto:
- Registro MCP modular: `internal/mcp/register.go`
- Runtime MCP: `internal/mcp/server.go` e `internal/mcp/tools.go`
- Config por cliente: `app/agent_config.go`
- Fallback local bootstrap: `app/installer.go`
- Download de artefatos P2P: `app/p2p_bridge.go` e `app/p2p_chunks.go`
- Persistencia e trilha: `internal/database/sqlite.go`
- Saude e auto-recuperacao: `internal/watchdog/watchdog.go`

## Fases de Implementacao
Fase 1 - Refatoracao sem impacto funcional
- Quebrar registro monolitico em modulos internos.
- Habilitar/desabilitar modulo por policy.

Fase 2 - Runtime WASM
- Adicionar loader WASM e API host minima.
- Migrar 1-2 modulos de baixo risco.

Fase 3 - Seguranca completa
- Validacao obrigatoria de assinatura/hash.
- Auditoria e bloqueio estrito.

Fase 4 - Operacao e rollout
- Canary por cliente.
- Rollback automatico e observabilidade.

## Criterios de Aceite
1. Cliente A e Cliente B podem receber conjuntos diferentes de modulos sem rebuild do app.
2. Modulo invalido (assinatura/hash) e bloqueado obrigatoriamente.
3. Falha de modulo nao derruba o servidor MCP do Agent.
4. Rollback restaura ultima versao estavel sem reinstalacao do app.
5. Baseline do app continua funcional sem modulos opcionais.

## Notas Importantes
- Nao usar DLL como mecanismo principal de extensao neste contexto.
- Evitar gravar artefatos na pasta do executavel principal.
- Tratar modulo como conteudo versionado e auditavel, nao como dependencia nativa dinamica.
