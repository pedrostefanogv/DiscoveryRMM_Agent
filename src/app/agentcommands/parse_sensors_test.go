package agentcommands

import "testing"

// TestIsRunSensorsCommandType cobre o wire "runsensors" e o alias "runsensor".
func TestIsRunSensorsCommandType(t *testing.T) {
	accepted := []string{"runsensors", "RunSensors", " runsensor ", "runsensor"}
	for _, value := range accepted {
		if !IsRunSensorsCommandType(value) {
			t.Fatalf("IsRunSensorsCommandType(%q) = false, want true", value)
		}
	}
	rejected := []string{"", "run", "sensors", "p2ppreload", "update"}
	for _, value := range rejected {
		if IsRunSensorsCommandType(value) {
			t.Fatalf("IsRunSensorsCommandType(%q) = true, want false", value)
		}
	}
}

// TestParseRunSensorsPayload cobre payload objeto, JSON em string e nulo.
func TestParseRunSensorsPayload(t *testing.T) {
	command := ParseRunSensorsPayload(map[string]any{
		"sensorIds": []any{" a ", "", "b", "a"},
		"forceSync": true,
	})
	if len(command.SensorIDs) != 2 || command.SensorIDs[0] != "a" || command.SensorIDs[1] != "b" {
		t.Fatalf("sensorIds = %#v, want [a b] (sem vazios/duplicados)", command.SensorIDs)
	}
	if !command.ForceSync {
		t.Fatal("forceSync deveria ser true")
	}

	fromString := ParseRunSensorsPayload("{\"sensorIds\":[\"x\"]}")
	if len(fromString.SensorIDs) != 1 || fromString.SensorIDs[0] != "x" {
		t.Fatalf("payload em string: %#v", fromString.SensorIDs)
	}

	semFiltro := ParseRunSensorsPayload(nil)
	if len(semFiltro.SensorIDs) != 0 || semFiltro.ForceSync {
		t.Fatalf("payload nulo = %#v, want vazio", semFiltro)
	}

	quebrado := ParseRunSensorsPayload("{invalido")
	if len(quebrado.SensorIDs) != 0 {
		t.Fatalf("payload invalido deveria virar vazio: %#v", quebrado)
	}
}
