# Sistema de Watchdog - Discovery

## Visão Geral

O sistema de watchdog foi implementado para detectar e recuperar automaticamente de travamentos no aplicativo Discovery, especialmente quando está minimizado no system tray.

## Componentes Monitorados

### 1. **Tray (System Tray Icon)**
- **Heartbeat**: A cada 20 segundos
- **Problema**: Pode travar e parar de responder a cliques
- **Recuperação**: Limitada (systray não suporta restart completo), mas tenta atualizar o estado

### 2. **AI Service (Serviço de IA)**
- **Heartbeat**: A cada token/status durante streaming
- **Problema**: Streaming pode travar sem enviar tokens
- **Recuperação**: Cancela stream automaticamente e emite erro
- **Timeout de Stream**: 90 segundos sem atividade

### 3. **Agent Connection (Conexão com Servidor)**
- **Heartbeat**: A cada 25 segundos
- **Problema**: Conexão pode ficar travada
- **Recuperação**: Verifica status e aguarda reconexão automática do runtime

### 4. **Inventory (Inventário)**
- **Heartbeat**: Durante coleta de inventário (a cada 20s)
- **Problema**: Coleta pode travar em queries grandes
- **Recuperação**: Limpa cache para forçar refresh na próxima requisição

### 5. **UI Runtime (Execução da Interface)**
- **Heartbeat**: Pode ser adicionado conforme necessário
- **Problema**: Travamento geral da UI
- **Recuperação**: Configurável

## Arquitetura

### Backend (Go)

#### `internal/watchdog/watchdog.go`
- **Watchdog**: Estrutura principal que gerencia monitoramento
- **Config**: Configurações de intervalos e thresholds
- **HealthCheck**: Representa status de saúde de um componente
- **RecoveryAction**: Função chamada quando componente fica unhealthy

**Configuração Padrão:**
```go
HeartbeatInterval:   30s  // Frequência esperada de heartbeats
UnhealthyThreshold:  90s  // Sem heartbeat = unhealthy
DegradedThreshold:   60s  // Sem heartbeat = degraded
CheckInterval:       15s  // Frequência de health checks
EnableAutoRecovery:  true // Recuperação automática ativa
MaxRecoveryAttempts: 3    // Máximo de tentativas de recovery
```

**Status de Saúde:**
- `healthy`: Componente respondendo normalmente
- `degraded`: Heartbeat atrasado, mas ainda dentro do limite
- `unhealthy`: Sem heartbeat há muito tempo, necessita intervenção
- `unknown`: Componente não registrado ou sem informações

#### `internal/watchdog/stream.go`
- **StreamMonitor**: Monitora streams de longa duração (ex: AI chat)
- **OperationMonitor**: Wrapper para operações com timeout automático
- **PeriodicHeartbeat**: Envia heartbeats periódicos para componentes de longa duração

#### `app.go` - Integração
```go
// Startup
watchdogSvc.Start(ctx)
registerWatchdogRecovery()

// Goroutines críticas com heartbeat
watchdog.SafeGoWithContext(ctx, "inventory-startup", watchdogSvc, watchdog.ComponentInventory, func(ctx) {
    heartbeat := watchdog.NewPeriodicHeartbeat(watchdogSvc, watchdog.ComponentInventory, 20*time.Second)
    heartbeat.Start(ctx)
    defer heartbeat.Stop()
    
    // Operação de longa duração...
})

// AI Streaming com monitor
streamMonitor := watchdog.NewStreamMonitor("ai-chat-stream", 90*time.Second, onStallCallback)
streamMonitor.Start(ctx)
defer streamMonitor.Stop()

// Registrar atividade em cada token
streamMonitor.Activity()
```

#### Recovery Actions
Definidas em `registerWatchdogRecovery()`:
- **Tray**: Atualiza estado visual
- **Agent**: Verifica status de conexão
- **AI**: Cancela stream travado
- **Inventory**: Limpa cache
- **Callback Global**: Emite evento `watchdog:unhealthy` para frontend

### Frontend (JavaScript)

#### `frontend/index.html`
Seção de monitoramento na aba Debug:
- Listagem de componentes monitorados
- Status visual (healthy/degraded/unhealthy)
- Indicador de auto-recuperação
- Botão de atualização manual

#### `frontend/app.js`
```javascript
// Polling automático a cada 15s na aba Debug
startWatchdogPoll()

// Listener de eventos de unhealthy
window.runtime.EventsOn('watchdog:unhealthy', onWatchdogUnhealthy)

// Renderização de status
renderWatchdogHealth(checks)
```

#### `frontend/styles.css`
- Cards visuais por componente
- Cores por status (verde/amarelo/vermelho/cinza)
- Animação de pulse para componentes unhealthy
- Badges de recuperabilidade

## API Pública

### Backend (Go)

```go
// Enviar heartbeat manual
watchdog.Heartbeat(component Component)

// Executar função protegida com panic recovery
watchdog.SafeGo(name string, fn func())

// Executar goroutine com context e heartbeat
watchdog.SafeGoWithContext(ctx, name, watchdog, component, fn)

// Criar monitor de stream
monitor := watchdog.NewStreamMonitor(name, timeout, onStall)
monitor.Start(ctx)
monitor.Activity() // Registrar atividade
monitor.Stop()

// Criar monitor de operação com timeout
opMon := watchdog.NewOperationMonitor(name, timeout, watchdog, component)
err := opMon.Run(ctx, func(ctx) error { /* operação */ })

// Registrar recovery action
watchdog.RegisterRecovery(component, func(c Component) error {
    // Lógica de recuperação
    return nil
})

// Callback para eventos unhealthy
watchdog.OnUnhealthy(func(check HealthCheck) {
    // Handler personalizado
})

// Obter status (exposto ao frontend)
checks := app.GetWatchdogHealth()
```

### Frontend (JavaScript)

```javascript
// API exposta ao frontend (via Wails bindings)
appApi().GetWatchdogHealth()
  .then(checks => {
    // Array de HealthCheck objects
    // { component, status, message, lastBeat, checkedAt, recoverable }
  })

// Evento de componente unhealthy
window.runtime.EventsOn('watchdog:unhealthy', function(data) {
  // { component, status, message, recoverable }
})
```

## Fluxo de Detecção e Recuperação

```
1. Componente inicia → Envia heartbeat inicial
                      ↓
2. Watchdog registra timestamp do heartbeat
                      ↓
3. A cada 15s, watchdog verifica todos os componentes
                      ↓
4. Se elapsed > DegradedThreshold (60s)
   → Status = degraded
   → Log de AVISO
                      ↓
5. Se elapsed > UnhealthyThreshold (90s)
   → Status = unhealthy
   → Log de ALERTA
   → Emite evento watchdog:unhealthy para frontend
   → Se EnableAutoRecovery = true:
      ↓
      Chama RecoveryAction registrada
      ↓
      Se sucesso:
         → Reset contador tentativas
         → Volta a monitorar
      ↓
      Se falha:
         → Incrementa contador tentativas
         → Log de erro
         → Se tentativas < MaxRecoveryAttempts:
            → Aguarda próximo check e tenta novamente
         → Se tentativas >= MaxRecoveryAttempts:
            → Para de tentar recovery automático
            → Fica aguardando intervenção manual
```

## Proteção Contra Panics

Todas as goroutines críticas são envolvidas em `SafeGo` ou `SafeGoWithContext`:

```go
defer func() {
    if r := recover(); r != nil {
        log.Printf("[watchdog] PANIC recuperado: %v\n%s", r, debug.Stack())
        // Continua execução do aplicativo
    }
}()
```

Isso garante que panics em goroutines não derrubem todo o aplicativo.

## Como Usar

### Adicionar Novo Componente Monitorado

1. **Definir componente constant:**
```go
// internal/watchdog/watchdog.go
const ComponentNovo Component = "novo_componente"
```

2. **Registrar recovery action:**
```go
// app.go - registerWatchdogRecovery()
a.watchdogSvc.RegisterRecovery(watchdog.ComponentNovo, func(component watchdog.Component) error {
    // Lógica de recuperação
    return nil
})
```

3. **Enviar heartbeats:**
```go
// Opção 1: Heartbeat manual
a.watchdogSvc.Heartbeat(watchdog.ComponentNovo)

// Opção 2: Heartbeat periódico automático
heartbeat := watchdog.NewPeriodicHeartbeat(a.watchdogSvc, watchdog.ComponentNovo, 30*time.Second)
heartbeat.Start(ctx)
defer heartbeat.Stop()
```

4. **Adicionar no frontend (opcional):**
```javascript
// frontend/app.js - formatComponentName()
var names = {
    // ...
    'novo_componente': 'Nome Amigável',
};
```

### Monitorar Operação de Longa Duração

```go
monitor := watchdog.NewOperationMonitor(
    "nome-operacao",
    5*time.Minute,
    a.watchdogSvc,
    watchdog.ComponentInventory,
)

err := monitor.Run(ctx, func(ctx context.Context) error {
    // Operação de longa duração
    // Heartbeats automáticos a cada 15s
    return operacaoLonga(ctx)
})
```

### Monitorar Stream

```go
streamMon := watchdog.NewStreamMonitor(
    "stream-name",
    60*time.Second, // Timeout sem atividade
    func() {
        // Callback quando stream trava
        log.Println("Stream travado!")
        cancelStream()
    },
)

streamMon.Start(ctx)
defer streamMon.Stop()

// Em cada evento do stream:
onToken := func(token string) {
    streamMon.Activity() // Registra atividade
    // processar token...
}
```

## Logs

O watchdog gera logs detalhados no formato:

```
[watchdog] iniciado (intervalo: 15s, degraded: 1m0s, unhealthy: 1m30s)
[watchdog] AVISO: inventory degraded - heartbeat atrasado em 1m5s (limite: 1m0s)
[watchdog] ALERTA: ai_service unhealthy - sem heartbeat ha 2m10s (limite: 1m30s)
[watchdog] tentando recuperar ai_service (tentativa 1/3)
[watchdog] ai_service recuperado com sucesso
[watchdog] PANIC em goroutine 'tray-main': runtime error: invalid memory address
[watchdog] parado
```

## Testes

Para testar o sistema:

1. **Simular travamento do AI stream:**
   - Fazer request AI muito longo sem resposta
   - Aguardar 90s
   - Watchdog deve cancelar automaticamente

2. **Verificar status no frontend:**
   - Abrir aba Debug
   - Ver seção "Monitor de Saúde (Watchdog)"
   - Componentes devem aparecer com status

3. **Simular componente unhealthy:**
   - Parar de enviar heartbeats em um componente
   - Aguardar 90s
   - Ver alerta no log e no frontend

## Limitações Conhecidas

1. **System Tray**: Biblioteca `systray` não suporta restart completo. Recovery é limitado a atualização de estado.

2. **Goroutines travadas em I/O**: Se uma goroutine travar em operação de I/O bloqueante sem context, não há como cancelar. Sempre use contexts com timeout.

3. **Memória**: O watchdog não monitora consumo de memória, apenas liveness de componentes.

4. **CPU**: Não há detecção de high CPU usage, apenas travamentos completos.

## Próximos Passos (Futuro)

- [ ] Monitoramento de uso de memória
- [ ] Detecção de CPU alta
- [ ] Métricas de performance (latência de operações)
- [ ] Histórico de health checks
- [ ] Alertas por email/webhook quando unhealthy
- [ ] Dashboard de métricas em tempo real
- [ ] Export de logs de watchdog para análise
- [ ] Integration tests automatizados

## Referências

- Código fonte: `internal/watchdog/`
- Integração: `app.go`, `tray.go`
- UI: `frontend/index.html`, `frontend/app.js`, `frontend/styles.css`
- Documentação Go contexts: https://go.dev/blog/context
- Panic recovery patterns: https://go.dev/blog/defer-panic-and-recover
