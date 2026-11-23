// Package power expõe o estado de energia da máquina (bateria × tomada) lido
// de GetSystemPowerStatus (kernel32), sem subprocessos.
//
// Motivo: o inventário, o heartbeat, o adiamento de deploy (automação) e o
// preflight de Windows Update precisam da MESMA leitura com a MESMA semântica
// de "desconhecido" (255). Antes havia três cópias da struct
// SYSTEM_POWER_STATUS com regras ligeiramente diferentes — inclusive uma que
// marcava "carregando" quando ACLineStatus == 0, que significa justamente
// "na bateria".
//
// Este arquivo é livre de build tag: tipos e regras puras ficam aqui e são
// testáveis em qualquer plataforma. Só a chamada de sistema vive em
// status_windows.go / status_other.go.
package power

// Valores documentados de SYSTEM_POWER_STATUS (winbase.h).
const (
	acLineOffline = 0
	acLineOnline  = 1
	acLineUnknown = 255

	// batteryFlagCharging: bit "carregando".
	batteryFlagCharging = 8
	// batteryFlagNoBattery: 128 = NO SYSTEM BATTERY (desktop/VM).
	batteryFlagNoBattery = 128
	// batteryFlagUnknown: 255 = o SO não sabe.
	batteryFlagUnknown = 255

	// lifePercentUnknown: 255 = percentual indisponível.
	lifePercentUnknown = 255
	// lifeTimeUnknown: 0xFFFFFFFF = tempo indisponível.
	lifeTimeUnknown = 0xFFFFFFFF
)

// Status é o estado de energia da máquina.
//
// Ponteiros nil significam DESCONHECIDO — nunca "falso". Quem consome precisa
// distinguir os dois: tratar 255 como "na bateria" bloquearia deploy sem motivo
// e trataria notebook real como desktop no painel.
type Status struct {
	// BatteryKnown indica que o SO respondeu sobre a presença de bateria
	// (BatteryFlag != 255). Sem isso, HasBattery é apenas o default false e
	// NÃO pode ser interpretado.
	BatteryKnown bool
	// HasBattery indica presença física de bateria (BatteryFlag != 128).
	HasBattery bool
	// OnAC é true quando a máquina está na tomada; nil = desconhecido.
	OnAC *bool
	// Charging é true quando a bateria está carregando; nil = desconhecido.
	Charging *bool
	// Percent é o percentual restante (0-100); nil = desconhecido.
	Percent *int
	// MinutesLeft é a autonomia estimada em minutos; nil = desconhecida.
	MinutesLeft *int
	// MinutesToFull é o tempo estimado até a carga completa; nil = desconhecido.
	MinutesToFull *int
}

// RawStatus é a leitura crua de SYSTEM_POWER_STATUS. Exposta para que o
// mapeamento (MapRaw) possa ser testado sem tocar no SO.
type RawStatus struct {
	ACLineStatus        byte
	BatteryFlag         byte
	BatteryLifePercent  byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

// MapRaw converte a leitura crua no estado normalizado. Função PURA.
func MapRaw(raw RawStatus) Status {
	var st Status

	switch raw.BatteryFlag {
	case batteryFlagUnknown:
		// 255: nada pode ser afirmado sobre a presença de bateria.
		st.BatteryKnown = false
		st.HasBattery = false
	case batteryFlagNoBattery:
		st.BatteryKnown = true
		st.HasBattery = false
	default:
		st.BatteryKnown = true
		st.HasBattery = true
	}

	switch raw.ACLineStatus {
	case acLineOffline:
		v := false
		st.OnAC = &v
	case acLineOnline:
		v := true
		st.OnAC = &v
	default:
		// 255: desconhecido.
	}

	if st.BatteryKnown && st.HasBattery {
		charging := raw.BatteryFlag == batteryFlagCharging
		st.Charging = &charging
	}

	if raw.BatteryLifePercent != lifePercentUnknown && raw.BatteryLifePercent <= 100 {
		p := int(raw.BatteryLifePercent)
		st.Percent = &p
	}
	if raw.BatteryLifeTime != lifeTimeUnknown {
		m := int(raw.BatteryLifeTime / 60)
		st.MinutesLeft = &m
	}
	if raw.BatteryFullLifeTime != lifeTimeUnknown {
		m := int(raw.BatteryFullLifeTime / 60)
		st.MinutesToFull = &m
	}

	return st
}

// OnBattery informa, de forma CONSERVADORA, se a máquina está sem energia AC.
// API indisponível ou ACLineStatus desconhecido devolvem false: nenhum
// consumidor deve bloquear deploy/update por um sinal incerto — era o
// comportamento dos call sites originais e é preservado aqui.
func OnBattery() bool {
	st, ok := Read()
	if !ok || st.OnAC == nil {
		return false
	}
	return !*st.OnAC
}
