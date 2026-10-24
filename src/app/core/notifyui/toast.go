package notifyui

// ToastRequest descreve uma notificação toast do Windows.
type ToastRequest struct {
	// AppID é o nome exibido pelo Windows (identidade global do agente).
	AppID string
	// IconPath é o caminho absoluto do PNG/ICO exibido ao lado do texto.
	// Vazio usa o ícone default do Windows.
	IconPath string
	// Title é o título (negrito).
	Title string
	// Message é o corpo do toast.
	Message string
	// Silent desativa o som.
	Silent bool
}

// DefaultToastAppID é a identidade global das notificações do agente —
// um único AppID vale para todos os clientes/agentes do servidor.
const DefaultToastAppID = "Discovery Agent"
