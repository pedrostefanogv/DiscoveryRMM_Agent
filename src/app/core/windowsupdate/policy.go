package windowsupdate

import "time"

// PolicySettings e o subconjunto de politica de Windows Update que o agente
// pode aplicar (fase 4, opt-in). Todos os campos sao ponteiros: nil = nao
// mexer no valor.
type PolicySettings struct {
	// NoAutoUpdate desabilita o auto-update do SO (1) ou devolve o padrao (0).
	NoAutoUpdate *uint32
	// AUOptions define o comportamento (2=notificar, 3=baixar e notificar,
	// 4=agendar instalacao).
	AUOptions *uint32
	// DeferQualityUpdates adia quality updates.
	DeferQualityUpdates *uint32
	// DeferQualityUpdatesPeriodInDays define os dias de adiamento.
	DeferQualityUpdatesPeriodInDays *uint32
	// PauseQualityUpdates pausa quality updates.
	PauseQualityUpdates *uint32
	// PauseQualityUpdatesStartTime e o instante RFC3339 do inicio da pausa.
	PauseQualityUpdatesStartTime *string
}

// PolicyValue e o snapshot serializavel de um valor de registro.
type PolicyValue struct {
	Exists  bool     `json:"exists"`
	Type    string   `json:"type,omitempty"`
	String  string   `json:"string,omitempty"`
	Number  uint64   `json:"number,omitempty"`
	Binary  []byte   `json:"binary,omitempty"`
	Strings []string `json:"strings,omitempty"`
}

// PolicyBackup captura os valores anteriores antes de aplicar politica.
type PolicyBackup struct {
	CapturedAt time.Time              `json:"capturedAt"`
	Values     map[string]PolicyValue `json:"values"`
}
