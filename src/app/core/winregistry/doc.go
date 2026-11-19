// Package winregistry fornece navegacao e edicao do Registro do Windows para
// uso em sessoes remotas (aba "Registro" do acesso remoto).
//
// Postura de seguranca: FAIL-CLOSED. Nenhuma operacao toca o registro antes de
// passar por guard.go. O default do guard e RECUSAR; so o que estiver
// explicitamente permitido e executado. E o mesmo desenho de sysctrl/service_guard_windows.go.
//
// Este pacote e LEAF: nao importa discovery/app nem github.com/wailsapp/wails.
// A invariante TestF14_T1_* depende disso — o binario discovery-service.exe
// (SYSTEM, sem UI) carrega este pacote.
package winregistry
