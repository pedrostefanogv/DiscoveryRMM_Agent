package inventory

import (
	"testing"

	"discovery/app/core/models"
)

func TestComputeMachineScore_Baseline(t *testing.T) {
	// Referência: 16c/32t + 64 GB ≈ score 100 (não é teto)
	report := models.InventoryReport{
		Hardware: models.HardwareInfo{
			Cores:        16,
			LogicalCores: 32,
			MemoryGB:     64,
		},
	}
	score := computeMachineScore(report)
	if score != 100 {
		t.Errorf("referência 16c/32t + 64GB: esperado 100, obteve %d", score)
	}
}

func TestComputeMachineScore_Mid(t *testing.T) {
	// Cenário comum: 8c/16t + 16 GB ≈ 38
	report := models.InventoryReport{
		Hardware: models.HardwareInfo{
			Cores:        8,
			LogicalCores: 16,
			MemoryGB:     16,
		},
	}
	score := computeMachineScore(report)
	// CPU: (8 + 8*0.3)/20.8*100 ≈ 50
	// RAM: 16/64*100 = 25
	// Score = (50+25)/2 = 38
	if score < 35 || score > 41 {
		t.Errorf("cenário 8c/16t + 16GB: esperado ~38, obteve %d", score)
	}
}

func TestComputeMachineScore_Minimum(t *testing.T) {
	// PC mínimo: 2c/2t + 4 GB ≈ 8
	report := models.InventoryReport{
		Hardware: models.HardwareInfo{
			Cores:        2,
			LogicalCores: 2,
			MemoryGB:     4,
		},
	}
	score := computeMachineScore(report)
	// CPU: (2 + 0*0.3)/20.8*100 ≈ 9.6
	// RAM: 4/64*100 = 6.25
	// Score = (9.6+6.25)/2 ≈ 8
	if score < 1 {
		t.Errorf("score mínimo deve ser pelo menos 1, obteve %d", score)
	}
	if score < 7 || score > 10 {
		t.Errorf("cenário 2c/2t + 4GB: esperado ~8, obteve %d", score)
	}
}

func TestComputeMachineScore_HighEnd(t *testing.T) {
	// Servidor: 32c/64t + 128 GB — sem teto, escala linear ≈ 200
	report := models.InventoryReport{
		Hardware: models.HardwareInfo{
			Cores:        32,
			LogicalCores: 64,
			MemoryGB:     128,
		},
	}
	score := computeMachineScore(report)
	// CPU: (32 + 32*0.3)/20.8*100 = 200
	// RAM: 128/64*100 = 200
	// Score = (200+200)/2 = 200
	if score < 190 || score > 210 {
		t.Errorf("servidor 32c/64t + 128GB: esperado ~200, obteve %d", score)
	}
}

func TestComputeMachineScore_SingleCoreThreadExcess(t *testing.T) {
	// Hyper-threading apenas: 1c/2t + 8 GB
	report := models.InventoryReport{
		Hardware: models.HardwareInfo{
			Cores:        1,
			LogicalCores: 2,
			MemoryGB:     8,
		},
	}
	score := computeMachineScore(report)
	// CPU: (1 + 1*0.3)/20.8*100 ≈ 6.25
	// RAM: 8/64*100 = 12.5
	// Score = (6.25+12.5)/2 ≈ 9
	if score < 1 {
		t.Errorf("score deve ser pelo menos 1, obteve %d", score)
	}
}

func TestComputeMachineScore_SuperServer(t *testing.T) {
	// Servidor de grande porte: 64c/128t + 512 GB — deve escalar bem acima de 200
	report := models.InventoryReport{
		Hardware: models.HardwareInfo{
			Cores:        64,
			LogicalCores: 128,
			MemoryGB:     512,
		},
	}
	score := computeMachineScore(report)
	// CPU: (64 + 64*0.3)/20.8*100 = 400
	// RAM: 512/64*100 = 800
	// Score = (400+800)/2 = 600
	if score < 550 || score > 650 {
		t.Errorf("servidor 64c/128t + 512GB: esperado ~600, obteve %d", score)
	}
}
