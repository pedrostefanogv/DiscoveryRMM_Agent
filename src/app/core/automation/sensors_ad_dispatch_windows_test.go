//go:build windows

package automation

// F5 — SensorType.ADConsistency (19) no agente.
//
// O coletor (sensors_ad_windows.go) já existia e estava testado; o que faltava
// era o TIPO e o REGISTRO no dispatch. Estes testes travam justamente a ligação
// entre os dois: sem o case no switch, o servidor escalonaria o sensor e o
// agente responderia "tipo nao suportado" — o sensor nunca coletaria nada.

import (
	"context"
	"testing"
)

// O tipo chega do servidor como string "ADConsistency" (enum serializado como
// string) e também precisa ser reconhecido pelo número 19.
func TestTipoADConsistency_NoContrato(t *testing.T) {
	if SensorTypeADConsistency != SensorType("ADConsistency") {
		t.Fatalf("SensorTypeADConsistency = %q, want ADConsistency", SensorTypeADConsistency)
	}

	got, ok := sensorTypeByNumber[19]
	if !ok || got != SensorTypeADConsistency {
		t.Fatalf("sensorTypeByNumber[19] = %q (ok=%v), want ADConsistency", got, ok)
	}

	var porNome SensorType
	if err := porNome.UnmarshalJSON([]byte(`"ADConsistency"`)); err != nil || porNome != SensorTypeADConsistency {
		t.Fatalf("UnmarshalJSON(\"ADConsistency\") = %q, %v", porNome, err)
	}

	var porNumero SensorType
	if err := porNumero.UnmarshalJSON([]byte("19")); err != nil || porNumero != SensorTypeADConsistency {
		t.Fatalf("UnmarshalJSON(19) = %q, %v", porNumero, err)
	}
}

// O dispatch precisa reconhecer o tipo: nao pode cair no default
// ("sensor tipo ... nao suportado pelo agente"), que e o erro de coleta.
// Esta maquina de CI pode ou nao ser DC — em ambos os casos a resposta tem de
// ser uma coleta limpa (vazia fora de DC), nunca o erro de tipo desconhecido.
func TestDispatchADConsistency_NaoCaiNoDefault(t *testing.T) {
	cfg := newSensorCollectConfig(SensorTypeADConsistency, "", "", 10)

	collections := collectSensor(context.Background(), &cfg)
	if len(collections) != 1 {
		t.Fatalf("esperava 1 amostra, veio %d", len(collections))
	}

	if collections[0].Err != "" {
		t.Fatalf("tipo ADConsistency nao esta registrado no dispatch: %q", collections[0].Err)
	}

	// Fora de DC o coletor devolve coleta vazia sem erro — isso e legitimo e
	// diferente de falha de coleta.
	if !isDomainControlerLocal() {
		if len(collections[0].Channels) != 0 {
			t.Fatalf("em maquina que nao e DC esperava coleta vazia, veio %v", collections[0].Channels)
		}
		return
	}

	// Em DC, ad_collect_ok precisa existir: e o canal que impede o servidor de
	// tratar "nao perguntei" como "esta tudo certo".
	if _, existe := collections[0].Channels[ChannelADCollectOK]; !existe {
		t.Fatalf("em DC a coleta precisa publicar %q", ChannelADCollectOK)
	}
}
