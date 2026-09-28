package automation

import (
	"encoding/json"
	"testing"
)

func TestBuildExecutionMetadata_MergesWingetDecision(t *testing.T) {
	result := ExecutionResult{
		Success:      true,
		ExitCode:     0,
		ExitCodeSet:  true,
		MetadataJSON: `{"wingetDecision":{"skip":true,"benign":true,"decidedBy":"inventory-cache"}}`,
	}
	task := AutomationTask{TaskID: "t-1", Name: "Brave", ActionType: ActionInstallPackage, PackageID: "brave.brave"}

	raw := buildExecutionMetadata(task, TriggerTypeImmediate, "result", &result, nil)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("metadata invalido: %v", err)
	}
	decision, ok := decoded["wingetDecision"].(map[string]any)
	if !ok {
		t.Fatalf("wingetDecision nao foi mesclado: %s", raw)
	}
	if decision["decidedBy"] != "inventory-cache" || decision["skip"] != true {
		t.Fatalf("wingetDecision inesperado: %+v", decision)
	}
}

func TestDispatchPreloadPackages_DedupsSameList(t *testing.T) {
	svc := &Service{}
	calls := 0
	var received []PreloadPackage
	svc.SetPreloadHandler(func(packages []PreloadPackage) {
		calls++
		received = packages
	})

	list := []PreloadPackage{{PackageID: "brave.brave", ActionType: ActionInstallPackage}}
	svc.dispatchPreloadPackages(list)
	svc.dispatchPreloadPackages(list) // mesma assinatura dentro do TTL → não repete
	if calls != 1 {
		t.Fatalf("esperava 1 chamada (dedup), got %d", calls)
	}
	if len(received) != 1 || received[0].PackageID != "brave.brave" {
		t.Fatalf("lista repassada inesperada: %+v", received)
	}

	svc.dispatchPreloadPackages([]PreloadPackage{{PackageID: "7zip.7zip", ActionType: ActionUpdateOrInstallPackage}})
	if calls != 2 {
		t.Fatalf("lista diferente deveria reacionar, got %d", calls)
	}

	svc.dispatchPreloadPackages(nil)
	if calls != 2 {
		t.Fatalf("lista vazia nao deve chamar, got %d", calls)
	}
}

func TestPreloadPackagesSignature_OrderInsensitive(t *testing.T) {
	a := preloadPackagesSignature([]PreloadPackage{{PackageID: "b", ActionType: ActionInstallPackage}, {PackageID: "a", ActionType: ActionInstallPackage}})
	b := preloadPackagesSignature([]PreloadPackage{{PackageID: "a", ActionType: ActionInstallPackage}, {PackageID: "b", ActionType: ActionInstallPackage}})
	if a != b {
		t.Fatalf("assinatura deve ser ordem-insensivel: %q != %q", a, b)
	}
}
