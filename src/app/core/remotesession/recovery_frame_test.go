package remotesession

import (
	"testing"
	"time"
)

// makeQM cria um QualityManager em modo AUTO com qualidade controlada.
func makeQM(t *testing.T, imageQ int) *QualityManager {
	t.Helper()
	qm := NewQualityManager(QualityConfig{JpegQuality: imageQ, Fps: 15, ScaleFactor: 1.0})
	qm.SetManualMode(false)
	qm.SetImageQualityAuto(imageQ)
	return &qm
}

func TestRecoveryFrameNotTriggeredWithoutDegradation(t *testing.T) {
	// Qualidade já no topo e nunca degradou: não há nada a recuperar.
	qm := makeQM(t, imageQualityMax)
	if qm.ConsumeRecoveryFrame() {
		t.Fatal("não deve disparar recovery sem degradação prévia")
	}
}

func TestRecoveryFrameTriggersAfterRecoveryToTop(t *testing.T) {
	qm := makeQM(t, imageQualityMax)

	// 1) Degrada e ENVIA frames nessa qualidade.
	qm.SetImageQualityAuto(40)
	qm.RecordFrame(1000, time.Now())

	// Ainda degradado: não dispara (a qualidade não voltou ao topo).
	if qm.ConsumeRecoveryFrame() {
		t.Fatal("não deve disparar recovery enquanto a qualidade está baixa")
	}

	// 2) Qualidade volta ao TOPO.
	qm.SetImageQualityAuto(imageQualityMax)

	// Agora sim: dispara UMA vez.
	if !qm.ConsumeRecoveryFrame() {
		t.Fatal("deveria disparar recovery após voltar ao topo da escada")
	}
	// E consome o sinal: não repete.
	if qm.ConsumeRecoveryFrame() {
		t.Fatal("o sinal de recovery deve ser consumido (não repetir)")
	}
}

func TestRecoveryFrameRearmsAfterNewDegradation(t *testing.T) {
	qm := makeQM(t, imageQualityMax)

	// Ciclo 1: degrada -> recupera -> dispara.
	qm.SetImageQualityAuto(30)
	qm.RecordFrame(1000, time.Now())
	qm.SetImageQualityAuto(imageQualityMax)
	if !qm.ConsumeRecoveryFrame() {
		t.Fatal("ciclo 1: deveria disparar")
	}

	// Ciclo 2: nova degradação REARMA; recuperar deve disparar de novo.
	qm.SetImageQualityAuto(30)
	qm.RecordFrame(1000, time.Now())
	if qm.ConsumeRecoveryFrame() {
		t.Fatal("com qualidade baixa não deve disparar")
	}
	qm.SetImageQualityAuto(imageQualityMax)
	if !qm.ConsumeRecoveryFrame() {
		t.Fatal("ciclo 2: deveria disparar novamente após rearmar")
	}
}

// Degradação sem envio de frame não conta: só marca 'degraded' quem
// efetivamente enviou algo naquela qualidade (é o viewer que ficou com artefato).
func TestRecoveryFrameRequiresActualSend(t *testing.T) {
	qm := makeQM(t, imageQualityMax)

	qm.SetImageQualityAuto(40) // degrada, mas NÃO envia frame
	qm.SetImageQualityAuto(imageQualityMax)

	if qm.ConsumeRecoveryFrame() {
		t.Fatal("sem envio em qualidade baixa não há artefato a recuperar")
	}
}

// Meio da escada não recupera: recuperar em 60% reintroduziria o artefato logo
// depois. Só recupera no TOPO.
func TestRecoveryFrameOnlyAtTopOfLadder(t *testing.T) {
	qm := makeQM(t, imageQualityMax)

	qm.SetImageQualityAuto(20)
	qm.RecordFrame(1000, time.Now())
	qm.SetImageQualityAuto(60) // subiu, mas não até o topo

	if qm.ConsumeRecoveryFrame() {
		t.Fatal("não deve recuperar no meio da escada")
	}

	qm.SetImageQualityAuto(imageQualityMax)
	if !qm.ConsumeRecoveryFrame() {
		t.Fatal("deveria recuperar ao atingir o topo")
	}
}

func TestRecoveryFrameConcurrentAccess(t *testing.T) {
	// RecordFrame e ConsumeRecoveryFrame são chamados de goroutines distintas
	// (loop de captura vs ingestão). Correr com -race deve ficar limpo.
	qm := makeQM(t, imageQualityMax)
	done := make(chan struct{})

	go func() {
		for i := 0; i < 200; i++ {
			qm.RecordFrame(100, time.Now())
		}
		close(done)
	}()

	for i := 0; i < 200; i++ {
		qm.SetImageQualityAuto(20 + (i % 7 * 10))
		_ = qm.ConsumeRecoveryFrame()
	}
	<-done
}
