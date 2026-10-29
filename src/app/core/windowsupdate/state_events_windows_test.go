//go:build windows

package windowsupdate

// Parsing do XML do wevtapi + resumo dos eventos do WindowsUpdateClient.

import (
	"testing"
	"time"
)

const sampleWindowsUpdateEventXML = `
<?xml version="1.0" encoding="UTF-8"?>
<Event xmlns="http://schemas.microsoft.com/win/2004/08/events/event">
  <System><Provider Name="Microsoft-Windows-WindowsUpdateClient" /><EventID>43</EventID>
    <TimeCreated SystemTime="2026-10-16T21:09:57.5486892Z" /><EventRecordID>99</EventRecordID></System>
  <EventData><Data Name="updateTitle">Windows 11 Insider Preview Quality Update (KB5127690)</Data></EventData>
</Event>`

func TestParseWindowsUpdateEventXML(t *testing.T) {
	record, ok := parseWindowsUpdateEventXML(sampleWindowsUpdateEventXML)
	if !ok {
		t.Fatal("evento nao parseado")
	}
	if record.EventID != 43 {
		t.Fatalf("EventID=%d", record.EventID)
	}
	want := time.Date(2026, 10, 16, 21, 9, 57, 0, time.UTC)
	if record.Time == nil || !record.Time.Equal(want) {
		t.Fatalf("SystemTime=%v want %v", record.Time, want)
	}
}

func TestSummarizeWindowsUpdateEvents(t *testing.T) {
	downloaded := time.Date(2026, 10, 16, 20, 30, 0, 0, time.UTC)
	installed := time.Date(2026, 10, 16, 21, 9, 57, 0, time.UTC)
	rebootRaised := time.Date(2026, 10, 16, 21, 10, 30, 0, time.UTC)

	// Ordem invertida (mais recente primeiro), como devolve a query.
	records := []WindowsUpdateEventRecord{
		{EventID: 19, Time: &rebootRaised, Message: "Reinicio necessario para concluir a instalacao"},
		{EventID: 44, Time: &installed, Message: "Instalacao bem-sucedida: Windows 11 Insider Preview Quality Update (KB5127690)"},
		{EventID: 41, Time: &downloaded, Message: "Atualizacao baixada."},
	}
	activity := SummarizeWindowsUpdateEvents(records)
	if activity.LastInstallSuccess == nil || !activity.LastInstallSuccess.Equal(installed) {
		t.Fatalf("LastInstallSuccess=%v", activity.LastInstallSuccess)
	}
	if activity.DownloadInProgress {
		t.Fatal("instalacao bem-sucedida posterior ao download: download nao esta em andamento")
	}

	// Sem instalacao bem-sucedida depois do download: aguardando instalacao/reboot.
	fixedWindowsUpdateNow(t, downloaded.Add(5*time.Minute))
	activity = SummarizeWindowsUpdateEvents([]WindowsUpdateEventRecord{
		{EventID: 41, Time: &downloaded, Message: "Atualizacao baixada."},
	})
	if !activity.DownloadInProgress {
		t.Fatal("download sem instalacao posterior deve ficar em andamento")
	}

	// Download ANTIGO (usuario adiou a instalacao): nao pode ficar "Baixando"
	// para sempre nem disparar scans por mudanca.
	fixedWindowsUpdateNow(t, downloaded.Add(maxDownloadInProgressAge+time.Minute))
	activity = SummarizeWindowsUpdateEvents([]WindowsUpdateEventRecord{
		{EventID: 41, Time: &downloaded, Message: "Atualizacao baixada."},
	})
	if activity.DownloadInProgress {
		t.Fatalf("download com mais de %s nao pode contar como em andamento", maxDownloadInProgressAge)
	}
}

func TestReadWindowsUpdateEventsRealChannel(t *testing.T) {
	records, err := ReadWindowsUpdateEvents()
	if err != nil {
		t.Skipf("canal de eventos indisponivel: %v", err)
	}
	if len(records) == 0 {
		t.Skip("canal sem eventos")
	}
	for _, record := range records {
		if record.EventID == 0 {
			t.Fatalf("evento sem id: %+v", record)
		}
	}
	t.Logf("%d eventos lidos; primeiro=%+v", len(records), records[0])
	activity := SummarizeWindowsUpdateEvents(records)
	t.Logf("resumo=%+v", activity)
	if !activity.Read {
		t.Fatal("Read deveria ser true")
	}
}

// A porcentagem de download tem de vir dos EVENTOS no Windows 10 1903+ (o
// WindowsUpdate.log textual nao existe mais): sem isso o campo
// probe.downloadPercent ficava sempre vazio e o badge "Baixando" nao mostrava %.
func TestSummarizeWindowsUpdateEventsExtractsDownloadPercent(t *testing.T) {
	older := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	newer := older.Add(4 * time.Minute)
	fixedWindowsUpdateNow(t, newer.Add(time.Minute))

	records := []WindowsUpdateEventRecord{
		{EventID: 41, Time: &newer, Message: "Transferencia concluida: 45% (0 de 1)"},
		{EventID: 41, Time: &older, Message: "Download iniciado: 5%"},
	}
	activity := SummarizeWindowsUpdateEvents(records)
	if activity.LastDownloadProgress != 45 {
		t.Fatalf("progresso=%d want 45 (o evento mais recente manda)", activity.LastDownloadProgress)
	}
	if !activity.DownloadInProgress {
		t.Fatal("download recente deveria estar em andamento")
	}
}

// Mensagem sem porcentagem nao inventa progresso; fora da faixa 0-100 e ignorada.
func TestDownloadPercentFromMessage(t *testing.T) {
	cases := []struct {
		message string
		want    int
		ok      bool
	}{
		{"Baixando - 45% (0 de 1)", 45, true},
		{"Progresso: 0%", 0, true},
		{"Concluido 100%", 100, true},
		{"Transferindo 250%", 0, false},
		{"Atualizacao baixada.", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		got, ok := downloadPercentFromMessage(tc.message)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Fatalf("downloadPercentFromMessage(%q)=%d,%v want %d,%v", tc.message, got, ok, tc.want, tc.ok)
		}
	}
}
