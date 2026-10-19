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
	activity = SummarizeWindowsUpdateEvents([]WindowsUpdateEventRecord{
		{EventID: 41, Time: &downloaded, Message: "Atualizacao baixada."},
	})
	if !activity.DownloadInProgress {
		t.Fatal("download sem instalacao posterior deve ficar em andamento")
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
