package windowsupdate

import (
	"strings"
	"testing"
	"time"
)

// Amostra reduzida do WindowsUpdate.log desta maquina (evento 44/43 do
// WUClient): download antes, instalacao bem-sucedida depois.
func sampleWindowsUpdateLog() string {
	lines := []string{
		"2026-10-16 21:04:02.1234567 1172 1688 DownloadManager Baixando atualizacao 42% (0 de 1)",
		"2026-10-16 21:06:30.0000000 1172 1688 DownloadManager Baixando atualizacao 100% (1 de 1)",
		"2026-10-16 21:09:57.7654321 1172 1688 Agent   Atualizacao: Instalacao Bem-sucedida - Windows 11 Insider Preview Quality Update (KB5127690)",
	}
	return strings.Join(lines, "\n")
}

func TestParseWindowsUpdateLogActivity(t *testing.T) {
	activity := ParseWindowsUpdateLogActivity(sampleWindowsUpdateLog())
	if activity.LastInstallSuccess == nil {
		t.Fatal("LastInstallSuccess nao detectada")
	}
	want := time.Date(2026, 10, 16, 21, 9, 57, 0, time.UTC)
	if !activity.LastInstallSuccess.Equal(want) {
		t.Fatalf("LastInstallSuccess=%s want %s", activity.LastInstallSuccess, want)
	}
	if activity.DownloadInProgress {
		t.Fatal("download ja concluido com instalacao posterior nao pode ficar em andamento")
	}
}

func TestParseWindowsUpdateLogActivityDownloadInProgress(t *testing.T) {
	text := strings.Join([]string{
		"2026-10-18 10:12:00.0000000 1 2 DownloadManager Baixando - 0% (0 de 1)",
	}, "\n")
	activity := ParseWindowsUpdateLogActivity(text)
	if !activity.DownloadInProgress {
		t.Fatal("download sem instalacao posterior deve ficar em andamento")
	}
	if activity.LastDownloadProgress != 0 {
		t.Fatalf("progresso=%d want 0", activity.LastDownloadProgress)
	}
	if activity.LastInstallSuccess != nil {
		t.Fatal("nao pode haver instalacao bem-sucedida")
	}
}

func TestParseWindowsUpdateLogActivityEnglishAndStamps(t *testing.T) {
	text := strings.Join([]string{
		"2026-10-16T21:09:57Z 1172 1688 Agent Installation Successful: KB5127846",
	}, "\n")
	activity := ParseWindowsUpdateLogActivity(text)
	if activity.LastInstallSuccess == nil || !activity.LastInstallSuccess.Equal(time.Date(2026, 10, 16, 21, 9, 57, 0, time.UTC)) {
		t.Fatalf("LastInstallSuccess=%v", activity.LastInstallSuccess)
	}
}

func TestParseWindowsUpdateLogActivityEmpty(t *testing.T) {
	activity := ParseWindowsUpdateLogActivity("")
	if activity.LastInstallSuccess != nil || activity.LastInstallTime != nil || activity.DownloadInProgress {
		t.Fatalf("log vazio nao pode produzir atividade: %+v", activity)
	}
}

func TestDecodeWindowsUpdateLogUTF16(t *testing.T) {
	text := "2026-10-16 21:09:57 Instalacao Bem-sucedida KB5127690"
	raw := []byte{0xFF, 0xFE}
	for _, r := range text {
		raw = append(raw, byte(r), byte(r>>8))
	}
	decoded := decodeWindowsUpdateLog(raw)
	if !strings.Contains(decoded, "Instalacao Bem-sucedida") {
		t.Fatalf("decode UTF-16 falhou: %q", decoded)
	}
}

func TestKBFromServicingPackageName(t *testing.T) {
	cases := map[string]string{
		"Package_for_KB5127690~31bf3856ad364e35~amd64~~26120.1.1.0":  "KB5127690",
		"Package_for_RollupFix~31bf3856ad364e35~amd64~~19041.1.1.0":  "",
		"Microsoft-Windows-Foo-Package~31bf3856ad364e35~amd64~~10.0": "",
		"package_for_kb005034441~x~amd64~~1":                         "KB5034441",
	}
	for name, want := range cases {
		if got := kbFromServicingPackageName(name); got != want {
			t.Fatalf("kbFromServicingPackageName(%q)=%q want %q", name, got, want)
		}
	}
}
