# Security Policy

Este documento define o baseline de segurança para desenvolvimento, revisão e operação do projeto.

## Escopo

- Código-fonte e histórico Git.
- Dependências e cadeia de build.
- Credenciais, tokens e segredos de integração.
- Processo de resposta a incidentes.

## Princípios

- Nunca versionar segredos em texto puro.
- Menor privilégio para tokens/chaves.
- Rotação periódica de credenciais.
- Revisão por PR para mudanças sensíveis.

## Segredos e Credenciais

- Proibido commitar:
  - API keys reais
  - tokens de acesso
  - senhas
  - private keys
  - certificados com chave privada
- Usar variáveis de ambiente, secret store ou mecanismos seguros da plataforma.
- Em logs, mascarar valores sensíveis.
- Em testes, usar dados fictícios e claramente não produtivos.

## Proteções de Branch

As branches críticas (`dev`, `beta`, `release`, `lts`) devem manter:

- Merge somente por Pull Request.
- Aprovação obrigatória.
- Historico linear, sem merge commits.
- Pull Requests devem ser integrados por estrategia compativel com historico linear, preferencialmente `squash merge` ou `rebase merge`.
- Bloqueio de force push.
- Bloqueio de deleção.
- Regras válidas também para administradores.

## Dependências e Supply Chain

- Revisar dependências novas em PR.
- Preferir versões estáveis.
- Atualizar periodicamente dependências críticas.
- Investigar alertas de vulnerabilidade antes de release.

## Segurança em Código

- Validar e sanitizar entradas externas.
- Definir timeouts em chamadas de rede.
- Não confiar em dados vindos do cliente.
- Evitar exposição desnecessária de detalhes internos em erros.

## Segurança de Transporte

- Priorizar TLS para comunicação remota.
- Uso de HTTP puro somente em cenários locais e controlados.
- Tokens de autenticação devem ser de curta duração quando possível.

## Processo de Vulnerabilidade

Se você identificar uma falha de segurança:

1. Não abra issue pública com exploit detalhado.
2. Reporte de forma privada pelo GitHub Security Advisories, usando a opcao `Report a vulnerability` deste repositorio.
3. Inclua:
  - impacto
  - escopo
  - passo a passo de reproducao
  - sugestao de mitigacao
4. Aguarde orientação de divulgação responsável.

## Resposta a Incidente

Em caso de vazamento suspeito:

1. Revogar e rotacionar imediatamente chaves/tokens.
2. Avaliar impacto e período de exposição.
3. Corrigir causa raiz.
4. Publicar patch e orientar atualização.
5. Registrar pós-mortem com ações preventivas.

## Histórico Git e Dados Sensíveis

- Reescrever histórico não invalida credenciais já expostas.
- Sempre rotacionar segredos após qualquer suspeita de exposição.
- Evitar reutilização de tokens antigos.

## Checklist de Segurança por PR

1. Há segredo hardcoded?
2. Há endpoint sensível indevido?
3. Há validação e timeout adequados?
4. Há impacto em autenticação/autorização?
5. Há plano de rollback?
