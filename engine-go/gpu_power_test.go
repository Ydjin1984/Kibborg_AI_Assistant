package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseGPUPowerLimits(t *testing.T) {
	got := parseGPUPowerLimits("3090:280, RTX 3060 :120;4090: 350")
	want := []gpuPowerRule{
		{Match: "3090", Watts: 280},
		{Match: "rtx 3060", Watts: 120},
		{Match: "4090", Watts: 350},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rules = %+v, want %+v", got, want)
	}
}

func TestParseGPUPowerLimitsRejectsGarbage(t *testing.T) {
	for _, spec := range []string{"", "   ", ",", "3090:", ":280", "abc", "3090:10", "3090:2000", "3090:x"} {
		if rules := parseGPUPowerLimits(spec); len(rules) != 0 {
			t.Fatalf("%q дал %+v, ждали пусто", spec, rules)
		}
	}
}

func TestParseWattsReadsNvidiaSMIFormat(t *testing.T) {
	if w, err := parseWatts("350.00 W"); err != nil || w != 350 {
		t.Fatalf(`parseWatts("350.00 W") = %d, %v`, w, err)
	}
	if _, err := parseWatts("[N/A]"); err == nil {
		t.Fatal("[N/A] не должен парситься")
	}
}

func TestLoadConfigGPUPowerLimit(t *testing.T) {
	dir := t.TempDir()
	ini := filepath.Join(dir, "settings.ini")
	body := "GPU_POWER_LIMIT_W=3090:280\n"
	if err := os.WriteFile(ini, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := loadConfig(ini)
	rules := parseGPUPowerLimits(cfg.GPUPowerLimits)
	if len(rules) != 1 || rules[0].Match != "3090" || rules[0].Watts != 280 {
		t.Fatalf("rules = %+v, ждали [{3090 280}]", rules)
	}
}
