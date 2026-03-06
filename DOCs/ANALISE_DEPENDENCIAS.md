# Análise de Dependências - Projeto Meduza Discovery

**Data**: 6 de março de 2026  
**Status Global**: ✅ Estável | ⚠️ Com actualizações disponíveis

---

## 1. Versões Instaladas

| Componente | Versão Atual | Status |
|-----------|-------------|--------|
| Go | 1.26.0 | ✅ Atualizado |
| Wails CLI | v2.11.0 | ✅ Alinhado |
| Wails (go.mod) | v2.11.0 | ✅ Alinhado |
| Go (go.mod) | 1.25.0 | ⚠️ Desatualizar documento |

---

## 2. Análise de Dependências Diretas (Go)

### ✅ Status: TODAS Atualizadas

```
github.com/energye/systray       v1.0.3   ✅ Última versão
github.com/go-pdf/fpdf           v0.9.0   ✅ Última versão
github.com/gorilla/websocket     v1.5.3   ✅ Última versão
github.com/nats-io/nats.go       v1.38.0  ✅ Última versão
github.com/samber/lo             v1.52.0  ✅ Última versão
github.com/wailsapp/wails/v2     v2.11.0  ✅ Última versão
golang.org/x/sys                 v0.41.0  ✅ Última versão
```

---

## 3. Dependências Indiretas Desatualizadas

### 🔴 Críticas (Segurança/Funcionalidade)

| Pacote | Instalado | Disponível | Impacto |
|--------|-----------|-----------|--------|
| ProtonMail/go-crypto | v1.1.5 | v1.4.0 | Alta (criptografia) |
| cloudflare/circl | v1.3.7 | v1.6.3 | Média (criptografia) |
| golang.org/x/text | v0.34.0 | v0.35.0+ | Baixa (encoding) |

### 🟡 Medianas (Funcionalidade)

| Pacote | Instalado | Disponível | Motivo |
|--------|-----------|-----------|--------|
| alecthomas/chroma/v2 | v2.14.0 | v2.23.1 | Syntax highlighting |
| charmbracelet/glamour | v0.8.0 | v0.10.0 | Rendering Markdown |
| charmbracelet/lipgloss | v0.12.1 | v1.1.0 | UI styling (QUEBRA) |
| Microsoft/go-winio | v0.6.1 | v0.6.2 | Windows I/O |

### 🟢 Baixas (Opcional)

- dario.cat/mergo: v1.0.0 → v1.0.2
- bitfield/script: v0.24.0 → v0.24.1
- boombuler/barcode: v1.0.1 → v1.1.0

---

## 4. Frontend

❌ **Sem dependências npm detectadas**
- Diretório `frontend/` só contém HTML/CSS/JS manual
- `frontend/wailsjs/runtime/package.json` é auto-gerado pelo Wails (não editar)
- Não há `package.json` no root de frontend para gerenciar libs TypeScript/JS

**Recomendação**: Se precisar de libs frontend (React, Vue, etc), criar `frontend/package.json` futuro.

---

## 5. Recomendações de Ação

### 🟢 Imediato (Recomendado)

1. **Atualizar go.mod de 1.25.0 para 1.26.0**
   ```powershell
   # Editar go.mod manualmente ou via:
   go get -u && go mod tidy
   ```
   Motivo: Documentar versão mínima de Go usada/testada.

2. **Atualizar dependências críticas de criptografia**
   ```powershell
   go get -u github.com/ProtonMail/go-crypto
   go get -u github.com/cloudflare/circl
   go get -u golang.org/x/crypto
   go mod tidy
   ```
   Motivo: Patches de segurança e compatibilidade.

### 🟡 Curto Prazo (Próxima versão)

3. **Revisar breaking change em charmbracelet/lipgloss v1.1.0**
   - Se usado via dependências indiretas, testar após update
   - Versão v1.x pode quebrar se alguém usar API exposta internamente

4. **Update optativo de middling libs**
   ```powershell
   go get -u github.com/alecthomas/chroma/v2
   go get -u github.com/charmbracelet/glamour
   # Testar rendering após update
   ```

### 🔵 Futuro (Nice-to-have)

- Configurar dependabot (GitHub Actions) para PRs automáticas
- Manter CI/CD que rode testes antes de aceitar updates

---

## 6. Comando Único para Atualização Segura

```powershell
cd c:\Projetos\Discovery

# 1. Backup
git add . && git commit -m "backup antes de update de deps"

# 2. Update direto (safe, já estão atualizadas)
go get -u ./...

# 3. Sincronizar
go mod tidy

# 4. Build test
wails build -s -nsis

# 5. Commit
git add . && git commit -m "chore: atualizar dependências go e go.mod"
```

---

## 7. Status Geral do Projeto

| Aspecto | Status | Notas |
|--------|--------|-------|
| **Dependências Diretas** | ✅ Atualizadas | Nenhuma ação requerida |
| **Dependências Indiretas** | ⚠️ Desatualizadas | Seguro atualizar |
| **Go Version** | ✅ Compatível | Atualizar documentação |
| **Wails** | ✅ Alinhado | Nada a fazer |
| **Frontend** | ⚠️ Sem libs npm | Sem impacto atual |
| **Build** | ✅ Funcional | Testado até last commit |

---

## 8. Próximas Etapas Sugeridas

1. [ ] Atualizar `go` version em `go.mod` para `1.26`
2. [ ] Executar `go get -u ./...` e testar build
3. [ ] Avaliar breaking change em lipgloss v1.x se aplicável
4. [ ] Configurar GitHub Dependabot para monitoring contínuo
5. [ ] Documentar versão mínima Go suportada (1.26.0 recomendado)

---

**Gerado em**: 2026-03-06 14:30  
**Ferramenta**: go list -u -m all
