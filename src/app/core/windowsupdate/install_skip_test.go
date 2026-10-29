package windowsupdate

import "testing"

func TestClassifyMissingTarget(t *testing.T) {
	installed := map[string]bool{"kb:KB5034441": true}

	outcome, failed := ClassifyMissingTarget("kb:KB5034441", installed, 0)
	if failed || outcome.SkipReason != InstallSkipAlreadyInstalled {
		t.Fatalf("update instalado deveria ser skip already_installed; got %+v failed=%v", outcome, failed)
	}

	outcome, failed = ClassifyMissingTarget("guid:3c3f1f1a-1111-2222-3333-444455556666:1", installed, 0)
	if failed || outcome.SkipReason != InstallSkipNotOffered {
		t.Fatalf("update fora do catalogo deveria ser skip not_offered; got %+v failed=%v", outcome, failed)
	}

	outcome, failed = ClassifyMissingTarget("guid:3c3f1f1a-1111-2222-3333-444455556666:1", installed, 2)
	if !failed || outcome.Error == "" {
		t.Fatalf("busca com itens ilegiveis deveria virar FALHA; got %+v failed=%v", outcome, failed)
	}
	if outcome.SkipReason != "" {
		t.Fatalf("falha nao pode carregar skipReason: %+v", outcome)
	}
}

func TestStillPendingAfterInstall(t *testing.T) {
	installed := []InstallOutcome{
		{UpdateKey: "a"},
		{UpdateKey: "b"},
	}
	pending := map[string]bool{"b": true, "c": true}

	still := StillPendingAfterInstall(installed, pending)
	if len(still) != 1 || still[0] != "b" {
		t.Fatalf("esperava apenas b pendente; got %v", still)
	}
	if len(StillPendingAfterInstall(nil, pending)) != 0 {
		t.Fatal("sem instalados nao ha pendentes a verificar")
	}
}
