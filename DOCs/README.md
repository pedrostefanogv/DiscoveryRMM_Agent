# DOCs - Mapa Consolidado

Este arquivo organiza a documentacao por tema e define a fonte canonica para reduzir duplicidade.

## Estado Consolidado por Documento

| Documento | Categoria | Estado | Acao recomendada | Fonte canonica |
|---|---|---|---|---|
| CONFIG_AGENTE.md | Configuracao local do agent | Ativo | Manter e evoluir | Este documento |
| GUIA_INSTALADOR_OFFLINE_ONLINE_API.md | Instalador | Duplicado parcial | Manter apenas como ponteiro | INSTALADOR_PAYLOAD_E_PARAMETROS.md |
| INSTALADOR_PAYLOAD_E_PARAMETROS.md | Instalador | Ativo | Manter e evoluir | Este documento |
| HISTORICO/PLANO_INSTALADOR_OFFLINE_ONLINE.md | Instalador | Historico | Arquivado como referencia | INSTALADOR_PAYLOAD_E_PARAMETROS.md |
| HISTORICO/OPCAO1_TEMPLATE_NSIS.md | Instalador | Historico conceitual | Arquivado como alternativa | INSTALADOR_PAYLOAD_E_PARAMETROS.md |
| GUIA_INTEGRACAO_PSADT_API_OPERACAO.md | PSADT e notificacoes | Ativo | Manter e evoluir | Este documento |
| HISTORICO/PLANO_INTEGRACAO_PSADT_NOTIFICACOES.md | PSADT e notificacoes | Historico | Arquivado como referencia | GUIA_INTEGRACAO_PSADT_API_OPERACAO.md |
| PLANO_EXECUCAO_OTIMIZACAO_INTEGRACAO_PSADT.md | PSADT e notificacoes | Ativo parcial | Manter como roadmap de execucao | GUIA_INTEGRACAO_PSADT_API_OPERACAO.md |
| p2p-api-contract.md | P2P (agente/local transport) | Ativo | Manter como contrato do agente | Este documento |
| P2P_SERVER_API_IMPLEMENTATION.md | P2P (servidor/normativo) | Ativo | Manter como fonte unica de validacoes backend | Este documento |
| MULTI_USER_SERVICE_GUIDE.md | Service mode multiusuario | Ativo parcial | Manter e revisar TODOs | Este documento |
| WATCHDOG_SYSTEM.md | Observabilidade e recuperacao | Ativo | Manter | Este documento |
| PLANO_MTLS_TLS_EXECUCAO_ASSINADA.md | Seguranca | Ativo parcial | Manter como plano faseado | Este documento |
| PLANO_RESILIENCIA_OFFLINE_SYNC.md | Resiliencia offline | Planejado | Manter como backlog | Este documento |
| PLANO_BUILD_MODULAR_MCP_WASM.md | MCP modular | Planejado | Manter como roadmap | Este documento |

## O que ja esta implementado e refletido no codigo

1. Instalador bootstrap online com suporte a ARG_BOOTSTRAP_INSTALL e ARG_PAYLOAD_URL.
2. Integracao de config PSADT e blocos de notificacao no parsing de configuracao.
3. Endpoints P2P do servidor no agente para seed-plan, telemetry e distribution-status.
4. Runtime de service mode com suporte a --service e IPC por Named Pipe.
5. Subsistema watchdog com codigo e testes dedicados.

## Pendencias relevantes ainda abertas

1. Renderer frontend de notificacoes ainda parcial no plano de execucao PSADT.
2. Rollback automatizavel por policy ainda pendente no backlog PSADT.
3. Resiliencia offline de resultados e telemetria ainda como plano (nao consolidada fim a fim).
4. Hardening completo de mTLS e execucao assinada ainda faseado.

## Regras de manutencao daqui para frente

1. Novo conteudo operacional deve entrar no documento canonico do tema.
2. Planos antigos nao devem receber novas seções funcionais; apenas status e links.
3. Ao concluir uma fase de plano, atualizar status no proprio plano e no quadro acima.
