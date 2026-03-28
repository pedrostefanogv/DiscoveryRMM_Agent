# Plano de Implementacao - mTLS Entre Agents + Transporte Estrito + Execucao Assinada

Data: 2026-03-27
Status: Aprovado para execucao faseada
Escopo: seguranca de transporte, identidade criptografica entre peers e execucao remota assinada

---

## 1. Decisoes consolidadas

1. mTLS entre agents sempre que possivel.
2. TLS forte tambem no caminho agent-servidor.
3. Producao em modo bloqueio para comandos/artifacts fora da politica.
4. AgentID com comprovacao criptografica no handshake.
5. Sem dependencia obrigatoria de recurso enterprise para iniciar hardening.

---

## 2. Objetivos de seguranca

1. Eliminar canais inseguros em producao (http/ws/nats sem TLS).
2. Impedir vazamento de credenciais por query string.
3. Reduzir risco de MITM no canal de controle e no P2P.
4. Impedir impersonacao de peer com binding AgentID <-> PeerID.
5. Exigir autenticidade e integridade de comandos e artifacts antes da execucao.

---

## 3. Fases de implementacao

## Fase 1 - Base de seguranca e feature flags (bloqueante)

### Features
1. Definir modos de seguranca: permissivo, auditoria, estrito e bloqueio.
2. Criar flags para:
   - enforcement de transporte seguro;
   - peer mutual auth;
   - execucao assinada.
3. Centralizar criacao de clients HTTP/WebSocket/NATS com politica unica.
4. Definir politica de rotacao/revogacao para credenciais e chaves.

### Verificacoes de seguranca
1. Checklist de configuracao por ambiente (dev/staging/prod).
2. Teste de regressao para garantir que modulo de debug nao contorna politica.
3. Auditoria de defaults: ambiente de producao nao pode iniciar em modo inseguro.

### Critero de saida
1. Politica unica de seguranca consumida por runtime e servicos de rede.

---

## Fase 2 - Transporte estrito agent-servidor

### Features
1. Remover access_token em query string no SignalR.
2. Manter credenciais apenas em Authorization header.
3. Em modo estrito, rejeitar em producao:
   - apiScheme diferente de https;
   - nats/ws sem transporte seguro.
4. Forcar TLS 1.2+ e validacao de cadeia/certificado no cliente.
5. Alinhar testes de conectividade de debug com as mesmas regras do runtime.

### Verificacoes de seguranca
1. Busca estatica para zero ocorrencias de token em query em fluxos de producao.
2. Testes negativos com endpoints remotos em http/ws e expectativa de bloqueio.
3. Teste de reconexao e fallback sem cair para transporte inseguro.
4. Validacao de logs para nao expor token.

### Impacto esperado
1. Reducao de exposicao de token em logs de proxy/WAF.
2. Reducao de vazamento via URL/historico.
3. Reducao de superficie para MITM.

---

## Fase 3 - Cloudflare e mTLS no caminho servidor

### Features
1. Curto prazo: Authenticated Origin Pulls no trecho Cloudflare -> origin, quando aplicavel.
2. Edge auth para agent: Cloudflare Access (service token/JWT) ou API Shield mTLS, conforme plano disponivel.
3. Se necessario mTLS ponta a ponta real agent -> origin sem terminacao no edge:
   - canal dedicado sem proxy;
   - ou tunel privado para trafego de controle sensivel.
4. Playbook de rotacao e revogacao de credenciais no Cloudflare e backend.

### Verificacoes de seguranca
1. Teste de bloqueio de acesso direto ao origin sem autenticacao valida.
2. Teste de expiracao/rotacao de credencial sem indisponibilidade prolongada.
3. Evidencia de que o fluxo proxied nao quebra garantias de autenticidade pretendidas.

### Observacao importante
Com dominio proxied, o TLS termina no edge. mTLS fim a fim puro exige caminho dedicado.

---

## Fase 4 - mTLS entre agents + binding de identidade

### Features
1. Emitir credencial curta assinada pelo backend contendo:
   - AgentID;
   - PeerID;
   - validade;
   - nonce.
2. Validar no handshake inbound/outbound:
   - assinatura da credencial;
   - validade temporal;
   - binding PeerID da conexao com PeerID da credencial;
   - consistencia AgentID anunciado versus credencial.
3. Bloquear peer com mismatch, expirado ou revogado.
4. Janela curta de compatibilidade para peers legados com prazo de desligamento.

### Verificacoes de seguranca
1. Testes de handshake valido/invalido/revogado.
2. Testes de replay com nonce e expiracao.
3. Testes de conflito AgentID -> PeerID (impersonacao) com bloqueio imediato.
4. Telemetria de bloqueios por motivo.

---

## Fase 5 - Execucao assinada + allowlist critica (block mode)

### Features
1. Envelope assinado obrigatorio para comando/tarefa com:
   - commandId;
   - agentId alvo;
   - issuer;
   - issuedAt/expiresAt;
   - nonce;
   - hash do payload;
   - assinatura.
2. Validar assinatura, expiracao, replay e hash antes da execucao.
3. Exigir hash assinado para artifact/installer.
4. Aplicar allowlist critica por:
   - tipo de acao;
   - executavel permitido;
   - path permitido;
   - flags permitidas/proibidas.
5. Em producao, falha de validacao/politica deve bloquear sem fallback.

### Verificacoes de seguranca
1. Testes unitarios para assinatura valida, invalida, expirada e replay.
2. Testes de integridade de artifact com hash adulterado.
3. Testes de bloqueio para comandos fora da allowlist.
4. Testes de auditoria para trilha completa de decisao.

---

## Fase 6 - Testes, rollout e observabilidade

### Features
1. Suite de testes de seguranca para transporte, handshake e execucao assinada.
2. Rollout por ondas curtas com feature flags.
3. Painel de observabilidade com indicadores de seguranca.

### Verificacoes de seguranca
1. Metricas minimas:
   - conexoes rejeitadas por transporte inseguro;
   - falhas de validacao de credencial de peer;
   - comandos bloqueados por assinatura/politica;
   - cert expirado/revogado.
2. Criterio de rollback restrito a incidente operacional critico.
3. Evidencias de hardening anexadas por release.

---

## 4. Mapa de arquivos-chave

1. internal/agentconn/runtime.go
2. app/debug/config.go
3. app/debug/service.go
4. netutil/headers.go
5. app/p2p_libp2p.go
6. app/p2p_libp2p_transport.go
7. app/p2p_cloud_bootstrap.go
8. app/p2p_onboarding.go
9. internal/automation/service.go
10. internal/automation/executor.go
11. app/automation_p2p.go
12. app/mesh.go
13. internal/database/sqlite.go

---

## 5. Backlog de implementacao por atividade

1. [TLS-001] Remover token de query no SignalR e manter Authorization header.
2. [TLS-002] Rejeitar transporte inseguro remoto no runtime (prod).
3. [TLS-003] Validar cadeia/certificado no cliente para HTTP/WS/NATS TLS.
4. [P2P-001] Persistir binding AgentID <-> PeerID confiavel.
5. [P2P-002] Bloquear mismatch AgentID/PeerID no handshake.
6. [P2P-003] Implementar revogacao/expiracao de credencial de peer.
7. [SIG-001] Criar envelope assinado para comandos/tarefas.
8. [SIG-002] Validar hash assinado de artifacts antes da execucao.
9. [SIG-003] Ativar allowlist critica em block mode.
10. [OBS-001] Dashboard de indicadores de seguranca e auditoria.

---

## 6. Matriz de verificacao (resumo)

1. Transporte remoto inseguro -> deve falhar antes de conectar.
2. Token em query string -> deve ser inexistente nos fluxos produtivos.
3. Peer com AgentID forjado -> handshake deve ser bloqueado.
4. Credencial expirada/revogada -> peer rejeitado.
5. Comando sem assinatura valida -> bloqueio imediato.
6. Artifact com hash divergente -> bloqueio imediato.

---

## 7. Status atual desta execucao

1. Implementacao inicial iniciada no runtime para endurecimento de transporte remoto.
2. Implementacao inicial iniciada no P2P registry para detectar conflito de identidade AgentID -> PeerID.
3. Proximos passos imediatos:
   - conectar validacao estrita de binding nos handshakes inbound/outbound;
   - cobrir com testes de regressao dos modulos alterados.
