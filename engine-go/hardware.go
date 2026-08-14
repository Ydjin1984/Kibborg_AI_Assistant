package main

// Тест железа для вкладки «Модели»: сокеты, ядра, потоки, RAM, карты и VRAM.
// Классификация «влезет на GPU / гибрид / только CPU» считается здесь же — и каталог,
// и Telegram (/hw) смотрят на одни цифры.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HardwareReport — снимок машины, на которой крутится агент.
type HardwareReport struct {
	At      time.Time   `json:"at"`
	CPU     []CPUInfo   `json:"cpu"`
	RAM     RAMInfo     `json:"ram"`
	GPUs    []GPUInfo   `json:"gpus"`
	Disks   []DiskInfo  `json:"disks"`
	Summary HardwareSum `json:"summary"`
	Notes   []string    `json:"notes,omitempty"`
	Source  string      `json:"source"`
}

// CPUInfo — один физический процессор (сокет). Два Xeon = две записи.
type CPUInfo struct {
	Name         string `json:"name"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Cores        int    `json:"cores"`
	Threads      int    `json:"threads"`
	Mhz          int    `json:"mhz,omitempty"`
}

// RAMInfo — физическая память. Total/Free в байтах, чтобы UI сам форматировал.
type RAMInfo struct {
	Total int64 `json:"total"`
	Free  int64 `json:"free"`
}

// GPUInfo — одна видеокарта. CUDA-ядра — справочник по имени: nvidia-smi их не отдаёт.
type GPUInfo struct {
	Index      int     `json:"index"`
	Name       string  `json:"name"`
	VRAMTotal  int64   `json:"vram_total"` // байты
	VRAMUsed   int64   `json:"vram_used"`
	VRAMFree   int64   `json:"vram_free"`
	UtilGPU    int     `json:"util_gpu"`
	UtilMem    int     `json:"util_mem"`
	TempC      int     `json:"temp_c,omitempty"`
	Driver     string  `json:"driver,omitempty"`
	Compute    string  `json:"compute,omitempty"`
	CUDACores  int     `json:"cuda_cores,omitempty"`
	VRAMTotalG float64 `json:"vram_total_gb"`
	VRAMFreeG  float64 `json:"vram_free_gb"`
}

// DiskInfo — локальный том. Нужен, чтобы сразу видеть, куда ляжет 20–70 ГБ GGUF.
type DiskInfo struct {
	Name  string  `json:"name"`
	FS    string  `json:"fs,omitempty"`
	Total int64   `json:"total"`
	Free  int64   `json:"free"`
	FreeG float64 `json:"free_gb"`
}

// HardwareSum — то, по чему фильтруется каталог: суммы, а не список карт.
type HardwareSum struct {
	Sockets   int     `json:"sockets"`
	Cores     int     `json:"cores"`
	Threads   int     `json:"threads"`
	RAMGB     float64 `json:"ram_gb"`
	RAMFreeGB float64 `json:"ram_free_gb"`
	GPUCount  int     `json:"gpu_count"`
	VRAMGB    float64 `json:"vram_gb"`
	VRAMFreeG float64 `json:"vram_free_gb"`
	CUDACores int     `json:"cuda_cores"`
}

// FitKind — куда лягут веса GGUF. KV-кэш контекста — отдельная пометка, не этот вердикт:
// «целиком на GPU» значит n-gpu-layers=99, а не «ещё и 32k контекста влезут».
const (
	fitGPU    = "gpu"
	fitHybrid = "hybrid"
	fitCPU    = "cpu"
	fitNo     = "no"
)

// FitResult — вердикт по одному файлу плюс числа, которые можно проверить глазами.
type FitResult struct {
	Kind     string  `json:"kind"`
	Label    string  `json:"label"`
	NeedGB   float64 `json:"need_gb"`
	VRAMGB   float64 `json:"vram_gb"`
	RAMGB    float64 `json:"ram_gb"`
	Note     string  `json:"note"`
	FileGB   float64 `json:"file_gb"`
	Headroom float64 `json:"headroom_gb"`
}

var (
	hwMu    sync.Mutex
	hwCache HardwareReport
	hwAt    time.Time
)

const hwCacheTTL = 20 * time.Second

// probeHardware снимает железо. force=true — кнопка «Тест железа», иначе берём кэш.
func probeHardware(force bool) HardwareReport {
	hwMu.Lock()
	if !force && !hwAt.IsZero() && time.Since(hwAt) < hwCacheTTL {
		rep := hwCache
		hwMu.Unlock()
		return rep
	}
	hwMu.Unlock()

	rep := HardwareReport{At: time.Now(), Source: "probe"}
	if runtime.GOOS == "windows" {
		probeWindowsHost(&rep)
	} else {
		rep.Notes = append(rep.Notes, "полный проб — на Windows; здесь только runtime.NumCPU")
		rep.CPU = []CPUInfo{{Name: runtime.GOARCH, Threads: runtime.NumCPU(), Cores: runtime.NumCPU()}}
	}
	probeNvidiaGPUs(&rep)
	summarizeHardware(&rep)

	hwMu.Lock()
	hwCache, hwAt = rep, time.Now()
	hwMu.Unlock()
	return rep
}

func summarizeHardware(rep *HardwareReport) {
	s := HardwareSum{Sockets: len(rep.CPU)}
	for _, c := range rep.CPU {
		s.Cores += c.Cores
		s.Threads += c.Threads
	}
	if s.Threads == 0 {
		s.Threads = runtime.NumCPU()
	}
	if s.Cores == 0 {
		s.Cores = s.Threads
	}
	s.RAMGB = bytesToGiB(rep.RAM.Total)
	s.RAMFreeGB = bytesToGiB(rep.RAM.Free)
	s.GPUCount = len(rep.GPUs)
	for _, g := range rep.GPUs {
		s.VRAMGB += g.VRAMTotalG
		s.VRAMFreeG += g.VRAMFreeG
		s.CUDACores += g.CUDACores
	}
	rep.Summary = s
}

// classifyFit решает, куда лягут веса файла. Запас 12% + 0.8 ГБ — рантайм llama.cpp
// (граф, буферы, драйвер). Две карты складываются: tensor-split как раз для этого.
func classifyFit(fileBytes int64, hw HardwareReport) FitResult {
	fileGB := bytesToGiB(fileBytes)
	need := fileGB * 1.08
	vram := hw.Summary.VRAMGB
	ram := hw.Summary.RAMGB
	out := FitResult{FileGB: round1(fileGB), NeedGB: round1(need), VRAMGB: round1(vram), RAMGB: round1(ram)}

	ramBudget := ram - 16
	if ramBudget < 8 {
		ramBudget = ram * 0.55
	}

	switch {
	case fileBytes <= 0:
		out.Kind, out.Label, out.Note = fitNo, "нет размера", "Hugging Face не отдал размер файла"
	case vram > 0 && need <= vram:
		out.Kind = fitGPU
		out.Label = "целиком на GPU"
		out.Headroom = round1(vram - need)
		out.Note = fmt.Sprintf("веса %.1f ГБ + запас 8%% = %.1f ≤ %.1f ГБ VRAM (%.0f ГБ свободно)",
			fileGB, need, vram, hw.Summary.VRAMFreeG)
	case vram > 0 && fileGB <= vram+ramBudget:
		out.Kind = fitHybrid
		out.Label = "гибрид GPU+CPU"
		out.Headroom = round1(vram*0.94 + ramBudget - fileGB)
		out.Note = fmt.Sprintf("веса %.1f ГБ больше VRAM (%.1f), остаток уйдёт в RAM (%.0f ГБ)",
			fileGB, vram, ram)
	case ram > 0 && fileGB+1.5 <= ram-8:
		out.Kind = fitCPU
		out.Label = "только CPU"
		out.Note = fmt.Sprintf("в VRAM (%.1f ГБ) не влезет, в RAM (%.0f ГБ) — да, будет медленно", vram, ram)
	default:
		out.Kind = fitNo
		out.Label = "не влезет"
		out.Note = fmt.Sprintf("нужно ~%.1f ГБ, в машине %.1f VRAM + %.0f RAM", need, vram, ram)
	}
	return out
}

func bytesToGiB(b int64) float64 {
	if b <= 0 {
		return 0
	}
	return float64(b) / (1024 * 1024 * 1024)
}

func giBToBytes(g float64) int64 {
	return int64(g * 1024 * 1024 * 1024)
}

// formatHardwareText — отчёт для Telegram и для /hw в чате панели.
func formatHardwareText(rep HardwareReport) string {
	var b strings.Builder
	b.WriteString("🖥 **Тест железа**\n\n")
	s := rep.Summary
	fmt.Fprintf(&b, "🧠 CPU: **%d** сокет · **%d** ядер · **%d** потоков\n", s.Sockets, s.Cores, s.Threads)
	for i, c := range rep.CPU {
		mhz := ""
		if c.Mhz > 0 {
			mhz = fmt.Sprintf(" · %d МГц", c.Mhz)
		}
		fmt.Fprintf(&b, "   %d. %s — %d ядер / %d потоков%s\n", i+1, c.Name, c.Cores, c.Threads, mhz)
	}
	fmt.Fprintf(&b, "💾 RAM: **%.0f ГБ** (свободно %.0f ГБ)\n", s.RAMGB, s.RAMFreeGB)
	if s.GPUCount == 0 {
		b.WriteString("🎮 GPU: не найдены (nvidia-smi молчит) — каталог покажет CPU/гибрид по RAM\n")
	} else {
		fmt.Fprintf(&b, "🎮 GPU: **%d** карт · **%.1f ГБ** VRAM суммарно · **%d** CUDA-ядер\n",
			s.GPUCount, s.VRAMGB, s.CUDACores)
		for _, g := range rep.GPUs {
			cores := ""
			if g.CUDACores > 0 {
				cores = fmt.Sprintf(" · %d CUDA", g.CUDACores)
			}
			fmt.Fprintf(&b, "   %d. **%s** — %.1f ГБ (свободно %.1f)%s · загрузка %d%%\n",
				g.Index, g.Name, g.VRAMTotalG, g.VRAMFreeG, cores, g.UtilGPU)
		}
	}
	if len(rep.Disks) > 0 {
		b.WriteString("💿 Диски:\n")
		for _, d := range rep.Disks {
			fmt.Fprintf(&b, "   %s — свободно %.0f из %.0f ГБ\n", d.Name, d.FreeG, bytesToGiB(d.Total))
		}
	}
	for _, n := range rep.Notes {
		b.WriteString("- ⚠️ " + n + "\n")
	}
	return b.String()
}

// ===== Windows host (CPU / RAM / диски) =====

type winHostJSON struct {
	CPU []struct {
		Name         string `json:"name"`
		Manufacturer string `json:"manufacturer"`
		Cores        int    `json:"cores"`
		Threads      int    `json:"threads"`
		Mhz          int    `json:"mhz"`
	} `json:"cpu"`
	RAMTotal int64 `json:"ram_total"`
	RAMFree  int64 `json:"ram_free"`
	Disks    []struct {
		Name  string `json:"name"`
		FS    string `json:"fs"`
		Total int64  `json:"total"`
		Free  int64  `json:"free"`
	} `json:"disks"`
}

func probeWindowsHost(rep *HardwareReport) {
	const ps = `$cpu = @(Get-CimInstance Win32_Processor | ForEach-Object { @{name=$_.Name.Trim(); manufacturer=$_.Manufacturer; cores=[int]$_.NumberOfCores; threads=[int]$_.NumberOfLogicalProcessors; mhz=[int]$_.MaxClockSpeed} }); $os = Get-CimInstance Win32_OperatingSystem; $cs = Get-CimInstance Win32_ComputerSystem; $disks = @(Get-CimInstance Win32_LogicalDisk -Filter "DriveType=3" | ForEach-Object { @{name=$_.DeviceID; fs=$_.FileSystem; total=[int64]$_.Size; free=[int64]$_.FreeSpace} }); @{cpu=$cpu; ram_total=[int64]$cs.TotalPhysicalMemory; ram_free=[int64]$os.FreePhysicalMemory*1024; disks=$disks} | ConvertTo-Json -Compress -Depth 5`
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		rep.Notes = append(rep.Notes, "WMI/CIM не ответил: "+err.Error())
		rep.CPU = []CPUInfo{{Name: "CPU", Threads: runtime.NumCPU(), Cores: runtime.NumCPU()}}
		return
	}
	raw := bytes.TrimSpace(stdout.Bytes())
	if len(raw) == 0 {
		rep.Notes = append(rep.Notes, "PowerShell вернул пустой снимок железа")
		rep.CPU = []CPUInfo{{Name: "CPU", Threads: runtime.NumCPU(), Cores: runtime.NumCPU()}}
		return
	}
	var host winHostJSON
	if err := json.Unmarshal(raw, &host); err != nil {
		rep.Notes = append(rep.Notes, "не разобрал снимок железа: "+err.Error())
		rep.CPU = []CPUInfo{{Name: "CPU", Threads: runtime.NumCPU(), Cores: runtime.NumCPU()}}
		return
	}
	for _, c := range host.CPU {
		rep.CPU = append(rep.CPU, CPUInfo{
			Name:         strings.TrimSpace(c.Name),
			Manufacturer: strings.TrimSpace(c.Manufacturer),
			Cores:        c.Cores,
			Threads:      c.Threads,
			Mhz:          c.Mhz,
		})
	}
	rep.RAM = RAMInfo{Total: host.RAMTotal, Free: host.RAMFree}
	for _, d := range host.Disks {
		rep.Disks = append(rep.Disks, DiskInfo{
			Name: d.Name, FS: d.FS, Total: d.Total, Free: d.Free, FreeG: round1(bytesToGiB(d.Free)),
		})
	}
}

// ===== NVIDIA =====

func probeNvidiaGPUs(rep *HardwareReport) {
	cmd := exec.Command("nvidia-smi",
		"--query-gpu=index,name,memory.total,memory.used,memory.free,utilization.gpu,utilization.memory,temperature.gpu,driver_version,compute_cap",
		"--format=csv,noheader,nounits")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		rep.Notes = append(rep.Notes, "nvidia-smi не найден или не ответил — VRAM неизвестна")
		return
	}
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := splitCSV(line)
		if len(parts) < 5 {
			continue
		}
		idx, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
		name := strings.TrimSpace(parts[1])
		totalMiB := atoiDefault(parts[2], 0)
		usedMiB := atoiDefault(parts[3], 0)
		freeMiB := atoiDefault(parts[4], 0)
		g := GPUInfo{
			Index:     idx,
			Name:      name,
			VRAMTotal: int64(totalMiB) * 1024 * 1024,
			VRAMUsed:  int64(usedMiB) * 1024 * 1024,
			VRAMFree:  int64(freeMiB) * 1024 * 1024,
			CUDACores: cudaCoresFor(name),
		}
		g.VRAMTotalG = round1(bytesToGiB(g.VRAMTotal))
		g.VRAMFreeG = round1(bytesToGiB(g.VRAMFree))
		if len(parts) > 5 {
			g.UtilGPU = atoiDefault(parts[5], 0)
		}
		if len(parts) > 6 {
			g.UtilMem = atoiDefault(parts[6], 0)
		}
		if len(parts) > 7 {
			g.TempC = atoiDefault(parts[7], 0)
		}
		if len(parts) > 8 {
			g.Driver = strings.TrimSpace(parts[8])
		}
		if len(parts) > 9 {
			g.Compute = strings.TrimSpace(parts[9])
		}
		rep.GPUs = append(rep.GPUs, g)
	}
}

func splitCSV(line string) []string {
	// nvidia-smi csv: "0, NVIDIA GeForce RTX 3060, 12288, ..."
	parts := strings.Split(line, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// cudaCoresFor — справочник. nvidia-smi ядра не печатает; без таблицы карточка врёт нулём.
func cudaCoresFor(name string) int {
	n := strings.ToLower(name)
	// более длинные имена первыми: 3060 Ti раньше 3060.
	table := []struct {
		sub   string
		cores int
	}{
		{"5090", 21760}, {"5080", 10752}, {"5070 ti", 8960}, {"5070", 6144},
		{"4090", 16384}, {"4080 super", 10240}, {"4080", 9728}, {"4070 ti super", 8448},
		{"4070 ti", 7680}, {"4070 super", 7168}, {"4070", 5888}, {"4060 ti", 4352}, {"4060", 3072},
		{"3090 ti", 10752}, {"3090", 10496}, {"3080 ti", 10240}, {"3080", 8704},
		{"3070 ti", 6144}, {"3070", 5888}, {"3060 ti", 4864}, {"3060", 3584},
		{"3050", 2560}, {"a6000", 10752}, {"a5000", 8192}, {"a4500", 7168},
		{"a4000", 6144}, {"a2000", 3328}, {"l40s", 18176}, {"l40", 18176},
		{"l4", 7424}, {"a100", 6912}, {"h100", 16896}, {"v100", 5120},
		{"p40", 3840}, {"titan rtx", 4608}, {"1660 ti", 1536}, {"1660", 1408},
		{"1650", 896}, {"2080 ti", 4352}, {"2080", 2944}, {"2070", 2304}, {"2060", 1920},
	}
	for _, t := range table {
		if strings.Contains(n, t.sub) {
			return t.cores
		}
	}
	return 0
}
