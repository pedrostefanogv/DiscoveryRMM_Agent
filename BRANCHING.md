# Branching e Release Policy

Este documento define o fluxo oficial de branches para desenvolvimento, validação, release e manutenção LTS do projeto.

## Objetivo

- Evitar commits diretos em branches protegidas.
- Garantir rastreabilidade de mudanças por Pull Request.
- Separar claramente novas features, estabilização e correções críticas.

## Branches Oficiais

- `main`: espelho estável do release atual (produção).
- `dev`: integração contínua de features.
- `beta`: candidata para validação funcional e regressão.
- `release`: preparação final da versão a publicar.
- `lts`: manutenção de longo prazo (somente correções críticas e segurança).

## Regras Gerais

- Todo trabalho começa em branch de curta duração (`feature/*`, `bugfix/*`, `hotfix/*`, `chore/*`).
- Merge sempre por Pull Request.
- Proibido push direto em `main`, `dev`, `beta`, `release`, `lts`.
- Rebase/squash permitido somente na branch de trabalho.
- PR deve estar verde (build/testes) antes de merge.

## Convenção de Branches de Trabalho

- `feature/<escopo>-<descricao-curta>`
- `bugfix/<escopo>-<descricao-curta>`
- `hotfix/<escopo>-<descricao-curta>`
- `chore/<escopo>-<descricao-curta>`

Exemplos:

- `feature/p2p-peer-cache`
- `bugfix/remote-debug-timeout`
- `hotfix/auth-header-null`

## Fluxo de Promoção

1. Feature/Bugfix -> `dev`
  - Base: `dev`
  - Destino: `dev`
  - Criterio: revisao + CI verde.

2. `dev` -> `beta`
  - Quando houver conjunto de mudancas pronto para validacao.
  - Criterio: smoke tests e validacao funcional do time.

3. `beta` -> `release`
  - Somente quando nao houver blocker.
  - Criterio: regressao aprovada e changelog atualizado.

4. `release` -> `main`
  - Publicacao oficial da versao.
  - Criar tag SemVer apos merge (ex.: `v1.4.0`).

5. `release` -> `lts`
  - Para versoes que terao suporte estendido.
  - A decisao de suporte LTS deve ser explicita na release.

## Hotfix e Segurança

1. Correções urgentes em produção
  - Criar `hotfix/*` a partir de `main` (ou `lts` quando aplicavel).
  - PR para `release` e `main`.
  - Cherry-pick para `dev` e `beta` para evitar divergencia.

2. Correções urgentes em LTS
  - Criar `hotfix/*` a partir de `lts`.
  - PR para `lts`.
  - Avaliar backport para `main`/`release`/`dev` caso relevante.

## Versionamento

- Adotar SemVer: `MAJOR.MINOR.PATCH`.
- `MAJOR`: quebra de compatibilidade.
- `MINOR`: funcionalidade nova compatível.
- `PATCH`: correção sem quebra de contrato.

## Tags e Canais de Release

- Release normal: `v1.2.3`
- Beta: `v1.2.3-beta.1`
- LTS: `v1.2.3-lts.1`

Regras operacionais:

- Tags `v*` disparam o pipeline automatizado de release no GitHub Actions.
- Tags com `-beta.` publicam release como prerelease.
- Tags com `-lts.` publicam release LTS sem marcar como latest.
- Tags sem sufixo (`v1.2.3`) representam release estavel normal e podem virar latest.

## Política de PR

- Mínimo 1 aprovação obrigatória.
- Resolver toda conversa antes do merge.
- Commits descritivos (preferência por Conventional Commits):
  - `feat:`
  - `fix:`
  - `refactor:`
  - `docs:`
  - `test:`
  - `chore:`

## Checklist de Release

1. CI verde na branch `release`.
2. Changelog atualizado.
3. Versão definida (SemVer).
4. Tag criada após merge em `main`.
5. Comunicação de release e impactos.
6. Se aplicável, backport para `lts`.

## Exemplo de Ciclo

1. Merge de features em `dev` ao longo da sprint.
2. Congelamento funcional e promoção para `beta`.
3. Correções finais em `beta` e promoção para `release`.
4. Publicação em `main` com tag.
5. Correções críticas posteriores entram por `hotfix/*`.
