# Multi-User Windows Service Setup Guide

## Overview

Discovery Agent pode agora ser executado como **Windows Service** para suportar múltiplos usuários em um mesmo PC, com o serviço rodando 24/7 independentemente de login.

### Arquitetura

```
┌─────────────────────────────────────────────────────┐
│         Windows Service (SYSTEM user)               │
│  discovery.exe --service                             │
│  ├─ Headless (sem UI Wails)                        │
│  ├─ Roda 24/7, mesmo sem login                     │
│  ├─ Escuta em \\.\pipe\Discovery_Service           │
│  ├─ Workers: automação, inventório, P2P            │
│  └─ Processador de comandos via Named Pipes        │
│                                                      │
│  ┌─────────────────────────────────────────────┐   │
│  │         UI App (Wails Tray)                  │   │
│  │  discovery.exe (sem flags)                   │   │
│  │  ├─ Roda em contexto do usuário logado      │   │
│  │  ├─ Autostart: Task Scheduler (Any User)    │   │
│  │  ├─ Delay no logon: 30s                      │   │
│  │  ├─ Conecta ao service via Named Pipe       │   │
│  │  ├─ Poll status a cada 30s                  │   │
│  │  └─ Single-instance por usuário/sessão      │   │
│  └─────────────────────────────────────────────┘   │
│                                                      │
│             Dados Compartilhados                     │
│  C:\ProgramData\Discovery\                           │
│  ├─ discovery.db                                    │
│  ├─ config.json (credenciais, agentId)             │
│  ├─ chat_config.json                               │
│  ├─ logs/                                           │
│                                                      │
│  Cache temporário P2P (Windows)                     │
│  C:\Windows\Temp\Discovery\P2P_Temp\               │
└─────────────────────────────────────────────────────┘
```

---

## Modo Service (Novo)

### Iniciar como Service

```bash
# Modo headless - roda sem UI, 24/7
discovery.exe --service

# Com arquivo de log customizado
discovery.exe --service --log-file=C:\ProgramData\Discovery\logs\service.log
```

### Features do Service

| Feature | Descrição |
|---------|-----------|
| **Automação** | Workers executam a cada 5 minutos |
| **Inventório** | Sincroniza em intervalo configurável |
| **P2P** | Descoberta e transfer contínua |
| **IPC** | Escuta comandos via Named Pipe |
| **Multi-user** | UI apps de múltiplos usuários se conectam |

---

## Modo UI (Wails - Existente)

### Iniciar normalmente

```bash
# UI normal com tray icon
discovery.exe

# Minimizado na startup (para autostart)
discovery.exe --startup-minimized

# MCP server mode
discovery.exe --mcp
```

### Conexão ao Service

Quando iniciado, o UI app tentar se conectar automaticamente ao service via Named Pipe `\\.\pipe\Discovery_Service`:

- ✅ Se service está rodando → mostra "Agent Healthy" no tray
- ❌ Se service não está rodando → continua em modo standalone

---

## Fluxos Operacionais

### Fluxo 1: Reboot sem Login

```
1. Windows inicia
2. Service Manager executa discovery.exe --service (SYSTEM)
3. Service carrega config de C:\ProgramData\Discovery\config.json
4. Inicia workers (automação, P2P, inventório)
5. Aguarda conexões de UI apps via Named Pipe
6. ✅ Agent funciona 24/7, mesmo sem usuário logado
```

### Fluxo 2: Usuário Loga

```
1. Task Scheduler (At log on of any user) dispara
2. Após 30s de delay, discovery.exe --startup-minimized inicia (user context)
3. Tenta conectar ao service via Named Pipe
4. ✅ Conecta com sucesso → mostra "Healthy" no tray
5. UI faz poll de status a cada 30 segundos
```

### Fluxo 3: Múltiplos Usuários

```
User1 Logado:
├─ discovery.exe UI inicia (conexão A ao pipe)
└─ Vê agentId = "agent-123" (de C:\ProgramData)

User2 Logado (simultaneamente via RDP):
├─ discovery.exe UI inicia (conexão B ao pipe)
└─ Vê MESMO agentId = "agent-123"

Service (background):
└─ Processa comandos de ambas conexões
   ✅ Múltiplas conexões simultâneas suportadas
```

### Fluxo 4: Logout → Login Novo Usuário

```
User1 Logout:
├─ UI app fecha
└─ Conexão A ao pipe se desconecta

Service continua rodando (background)

User2 Login:
├─ UI app iniciado
├─ Conecta ao pipe (conexão C)
└─ Mesmo agentId, mesma config
   ✅ Dados e configuração compartilhados via C:\ProgramData
```

---

## Configuração de Dados

### Localização (C:\ProgramData\Discovery)

Dados persistentes do agent ficam em **C:\ProgramData\Discovery** (compartilhado entre usuários):

```
C:\ProgramData\Discovery\
├── discovery.db              # SQLite database
├── config.json               # Credenciais bootstrap
├── chat_config.json          # Configuração IA
├── debug_config.json         # Config debug
├── logs/
│   ├── discovery-service.log # Logs do service
│   └── service-errors.log    # Erros
```

Cache temporário P2P no Windows (pode ser limpo pelo sistema quando necessário):

```
C:\Windows\Temp\Discovery\P2P_Temp\
```

### Compatibilidade Fallback

Se `C:\ProgramData\Discovery` não existir:
1. Tenta `%LOCALAPPDATA%\Discovery` (compatibilidade)
2. Manter ambos os caminhos funcionando

---

## Protocol: Service ↔ UI (Named Pipes)

### Comando: getStatus

```json
# Request (UI → Service)
{
  "id": "req-uuid-123",
  "command": "getStatus",
  "user_name": "DESKTOP\\pedro",
  "payload": null
}

# Response (Service → UI)
{
  "id": "req-uuid-123",
  "status": "success",
  "code": 200,
  "data": {
    "running": true,
    "uptime_seconds": 3600,
    "agent_id": "agent-uuid",
    "server_url": "https://discovery.msp.com",
    "p2p_enabled": true,
    "last_sync": "2026-03-22T10:30:00Z"
  }
}
```

### Comando: execute

```json
# Request (UI → Service)
{
  "id": "req-uuid-456",
  "command": "execute",
  "user_name": "DESKTOP\\pedro",
  "payload": {
    "action": "install_package",
    "package": "7zip"
  }
}

# Response (Service → UI)
{
  "id": "req-uuid-456",
  "status": "pending",
  "code": 202,
  "data": {
    "action_id": "action-456",
    "started_at": "2026-03-22T10:35:00Z",
    "status": "queued"
  }
}
```

---

## Códigos de Resposta

| Code | Status | Significado |
|------|--------|-------------|
| 200 | success | Comando executado com sucesso |
| 202 | pending | Ação enfileirada, processando |
| 400 | error | Request JSON inválido |
| 404 | error | Comando desconhecido |
| 503 | error | Service ainda iniciando |

---

## Testes Básicos

### Test 1: Service inicia sem erros

```bash
# Terminal 1: Iniciar Service
discovery.exe --service

# Output esperado:
# [SERVICE] Iniciando Discovery Agent como Windows Service
# [SERVICE.Manager] Iniciando...
# [SERVICE.Manager] IPC server iniciado em \\.\pipe\Discovery_Service
# [SERVICE.AutomationWorker] Iniciado
# [SERVICE.InventoryWorker] Iniciado
```

### Test 2: UI conecta ao Service

```bash
# Terminal 2: Iniciar UI (enquanto service roda)
discovery.exe

# Esperado:
# - Tray icon aparece
# - Status mostra "Agent Healthy"
# - Sem erros de conexão
```

### Test 3: Múltiplos UIs simultâneos

```bash
# Terminal 2: UI instância 1
discovery.exe

# Terminal 3: Outra instância (simular outro usuário)
discovery.exe

# Esperado:
# - Ambas conectam ao mesmo service
# - Veem MESMO agentId
# - Ambas fazem polling de status
```

---

## Troubleshooting

### Service não inicia

```bash
# Verificar se porta/pipe está em uso
netstat -ano | findstr :5000  # Se usar HTTP (futuro)

# Verificar permissões em C:\ProgramData\Discovery
icacls "C:\ProgramData\Discovery"

# Procure por:
# - (F) = Full Control
# - Qualquer usuário deve ter acesso
```

### UI não conecta ao service

```bash
# 1. Verificar se service está rodando
tasklist | findstr discovery

# 1.1 Verificar tarefa de autostart da UI
schtasks /query /tn "DiscoveryAgentUI"

# 2. Verificar logs do service
type C:\ProgramData\Discovery\logs\service-errors.log

# 3. Tentar conectar manualmente (em desenvolvimento)
# Usar cliente test para enviar comando getStatus
```

### Dados não sincronizados entre usuários

```bash
# 1. Verificar se arquivo está em C:\ProgramData
dir "C:\ProgramData\Discovery\"

# 2. Verificar permissões do SQLite database
icacls "C:\ProgramData\Discovery\discovery.db"

# 3. Verificar locks (SQLite WAL: journal_mode=WAL)
dir "C:\ProgramData\Discovery\discovery.db*"
```

---

## Próximas Fases (TODO)

- [ ] User context executor (tarefas como user via service)
- [ ] Health monitoring e auto-restart do service

---

## Referências de Código

| Arquivo | Responsabilidade |
|---------|------------------|
| [service_main.go](service_main.go) | Entry point `--service` |
| [internal/service/manager.go](internal/service/manager.go) | Lifecycle do service |
| [internal/service/ipc.go](internal/service/ipc.go) | Servidor Named Pipes |
| [internal/service/client.go](internal/service/client.go) | Cliente para UI |
| [main.go](main.go) | Detecção de flags |

---

## Build & Deployment

```bash
# Build normal
go build -o discovery.exe

# Test service mode
.\discovery.exe --service &
sleep 2
.\discovery.exe  # Abre UI

# Stop service (Ctrl+C na terminal de service)
```

---

**Última atualização**: 22 de março de 2026
