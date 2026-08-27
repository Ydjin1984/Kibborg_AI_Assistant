package main

// Потолок мощности GPU перед запуском мозга. Ночные BSOD 0x124 (WHEA, PCIe
// Surprise Down на корневом порту Xeon) случались именно в момент загрузки
// GGUF в VRAM: у 3090 пиковые транзиенты заметно выше паспортного TDP, и
// просадка роняет карту с шины. nvidia-smi -pl срезает вершину пика; инференс
// почти не теряет — он упирается в bandwidth памяти, а не в TDP.

import (
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
)

// gpuPowerRule: watts применяется к картам, чьё имя содержит Match (без регистра).
type gpuPowerRule struct {
	Match string
	Watts int
}

// parseGPUPowerLimits разбирает "3090:280, RTX 3060:120" (разделители , и ;).
// Мусорные части молча пропускаются: настройка не должна валить старт мозга.
func parseGPUPowerLimits(spec string) []gpuPowerRule {
	var out []gpuPowerRule
	for _, part := range strings.FieldsFunc(spec, func(r rune) bool { return r == ',' || r == ';' }) {
		part = strings.TrimSpace(part)
		i := strings.Index(part, ":")
		if i <= 0 || i == len(part)-1 {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(part[:i]))
		watts, err := strconv.Atoi(strings.TrimSpace(part[i+1:]))
		if name == "" || err != nil || watts < 50 || watts > 700 {
			continue
		}
		out = append(out, gpuPowerRule{Match: name, Watts: watts})
	}
	return out
}

// applyGPUPowerLimits применяет GPU_POWER_LIMIT_W к реальным картам через
// nvidia-smi. Вызывается перед КАЖДЫМ запуском llama-server: пик транзиентов
// именно на заливке весов в VRAM, а лимит сбрасывается при перезагрузке.
// Ошибки не фатальны: нет nvidia-smi или нет прав администратора — пишем в лог
// и поднимаем мозг дальше.
func applyGPUPowerLimits(cfg Config) {
	rules := parseGPUPowerLimits(cfg.GPUPowerLimits)
	if len(rules) == 0 {
		return
	}
	out, err := exec.Command("nvidia-smi", "--query-gpu=index,name,power.limit", "--format=csv,noheader").Output()
	if err != nil {
		log.Printf("[GPU] power limit: nvidia-smi не ответил (%v) — лимиты не применены", err)
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		fields := strings.SplitN(line, ",", 3)
		if len(fields) < 2 || fields[0] == "" {
			continue
		}
		idx := strings.TrimSpace(fields[0])
		name := strings.TrimSpace(fields[1])
		cur := ""
		if len(fields) > 2 {
			cur = strings.TrimSpace(fields[2])
		}
		low := strings.ToLower(name)
		for _, r := range rules {
			if !strings.Contains(low, r.Match) {
				continue
			}
			if now, perr := parseWatts(cur); perr == nil && now <= r.Watts {
				log.Printf("[GPU] %s: потолок %d Вт не нужен — текущий %d Вт ниже", name, r.Watts, now)
				break
			}
			if b, err := exec.Command("nvidia-smi", "-i", idx, "-pl", strconv.Itoa(r.Watts)).CombinedOutput(); err != nil {
				log.Printf("[GPU] ⚠ %s: -pl %d не применился: %v (%s) — нужны права администратора, "+
					"см. GPU_POWER_LIMIT_W в settings.ini", name, r.Watts, err, strings.TrimSpace(string(b)))
			} else {
				log.Printf("[GPU] %s: потолок мощности %d Вт (было %s)", name, r.Watts, orDash(cur))
			}
			break
		}
	}
}

// parseWatts читает ответ power.limit вида "350.00 W" / "[N/A]".
func parseWatts(s string) (int, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimSuffix(s, "w")
	s = strings.ReplaceAll(s, " ", "")
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("не число: %q", s)
	}
	return int(f), nil
}
