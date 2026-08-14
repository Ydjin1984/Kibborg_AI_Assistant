package main

import (
	"strings"
	"testing"
)

func TestClassifyFitGPUHybridCPU(t *testing.T) {
	hw := HardwareReport{Summary: HardwareSum{VRAMGB: 24, RAMGB: 176, VRAMFreeG: 22}}
	// 19.7 ГБ IQ4 — текущий мозг на двух 3060 — обязан быть GPU.
	got := classifyFit(giBToBytes(19.7), hw)
	if got.Kind != fitGPU {
		t.Fatalf("19.7 ГБ на 24 ГБ VRAM: %s (%s)", got.Kind, got.Note)
	}
	// 40 ГБ — гибрид: в 24 не влезет, в 176 RAM — да.
	got = classifyFit(giBToBytes(40), hw)
	if got.Kind != fitHybrid {
		t.Fatalf("40 ГБ: %s, ждали hybrid", got.Kind)
	}
	// Без видеокарт, но с RAM — CPU.
	cpuOnly := HardwareReport{Summary: HardwareSum{RAMGB: 64}}
	got = classifyFit(giBToBytes(12), cpuOnly)
	if got.Kind != fitCPU {
		t.Fatalf("12 ГБ без GPU: %s, ждали cpu", got.Kind)
	}
	// Больше машины.
	got = classifyFit(giBToBytes(400), hw)
	if got.Kind != fitNo {
		t.Fatalf("400 ГБ: %s, ждали no", got.Kind)
	}
	if classifyFit(0, hw).Kind != fitNo {
		t.Fatal("пустой файл не должен получать вердикт gpu")
	}
}

func TestCudaCoresFor3060(t *testing.T) {
	if n := cudaCoresFor("NVIDIA GeForce RTX 3060"); n != 3584 {
		t.Fatalf("3060 cores = %d, ждали 3584", n)
	}
	if n := cudaCoresFor("NVIDIA GeForce RTX 3060 Ti"); n != 4864 {
		t.Fatalf("3060 Ti must win over 3060: %d", n)
	}
	if cudaCoresFor("unknown blob") != 0 {
		t.Fatal("неизвестная карта не должна врать ядрами")
	}
}

func TestFormatHardwareText(t *testing.T) {
	rep := HardwareReport{
		CPU:     []CPUInfo{{Name: "Xeon E5-2696 v4", Cores: 22, Threads: 44}},
		Summary: HardwareSum{Sockets: 2, Cores: 44, Threads: 88, RAMGB: 176, GPUCount: 2, VRAMGB: 24, CUDACores: 7168},
		GPUs:    []GPUInfo{{Index: 0, Name: "RTX 3060", VRAMTotalG: 12, VRAMFreeG: 11, CUDACores: 3584}},
	}
	s := formatHardwareText(rep)
	for _, want := range []string{"44", "88", "176", "RTX 3060", "CUDA"} {
		if !strings.Contains(s, want) {
			t.Errorf("в отчёте нет %q:\n%s", want, s)
		}
	}
}
