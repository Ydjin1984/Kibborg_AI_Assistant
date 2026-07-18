**✅ ПОЛНЫЙ СТЕК НАСТРОЙКИ ПОД ТВОЁ ЖЕЛЕЗО (2× RTX 3060 12 ГБ + 176 ГБ RAM + Xeon)** ⚙️

### 1. Рекомендуемая модель
**Qwen3.6-35B-A3B UD-IQ4_XS.gguf** (~19.7 ГБ) — оптимально для твоего железа.

### 2. Прямые ссылки для скачивания (Unsloth — лучшие кванты)

**Основная модель (рекомендую):**
- **IQ4_XS** → [СКАЧАТЬ](https://huggingface.co/unsloth/Qwen3.6-35B-A3B-GGUF/resolve/main/Qwen3.6-35B-A3B-UD-IQ4_XS.gguf?download=true)

**Запасной (если IQ4_XS будет глючить):**
- **Q3_K_M** → ~17 ГБ [СКАЧАТЬ](https://huggingface.co/bartowski/Qwen_Qwen3.6-35B-A3B-GGUF/resolve/main/Qwen3.6-35B-A3B-Q3_K_M.gguf?download=true)

**mmproj (для Vision — графики, скрины TradingView):**
- [mmproj-BF16.gguf](https://huggingface.co/unsloth/Qwen3.6-35B-A3B-GGUF/resolve/main/mmproj-BF16.gguf?download=true)

### 3. Команда для скачивания (самый удобный способ)
```bash
pip install -U "huggingface_hub[cli]"

# Основной вариант (IQ4_XS)
huggingface-cli download unsloth/Qwen3.6-35B-A3B-GGUF \
  --include "Qwen3.6-35B-A3B-UD-IQ4_XS.gguf" "mmproj-BF16.gguf" \
  --local-dir ./Qwen3.6-35B-A3B
```

### 4. Полная команда запуска llama-server (оптимизировано под dual 3060)

```bash
./llama-server \
  -m Qwen3.6-35B-A3B-UD-IQ4_XS.gguf \
  --mmproj mmproj-BF16.gguf \
  --ctx-size 32768 \
  --n-gpu-layers 99 \
  --tensor-split 0.5,0.5 \
  --main-gpu 0 \
  --cache-type-k q8_0 \
  --cache-type-v q8_0 \
  --flash-attn \
  --threads 28 \
  --port 8080 \
  --no-mmap \
  --temp 0.7 \
  --top-p 0.95 \
  --repeat-penalty 1.1
```

### 5. Альтернатива — Gemma 4 26B A4B (если Qwen всё равно упрётся)

**Ссылка на IQ4_XS (~14.2 ГБ):**
- [СКАЧАТЬ Gemma](https://huggingface.co/bartowski/google_gemma-4-26B-A4B-it-GGUF/resolve/main/google_gemma-4-26B-A4B-it-IQ4_XS.gguf?download=true)

**Команда запуска:**
```bash
./llama-server \
  -m google_gemma-4-26B-A4B-it-IQ4_XS.gguf \
  --ctx-size 32768 \
  --n-gpu-layers 99 \
  --tensor-split 0.5,0.5 \
  --threads 28 \
  --port 8080 \
  --flash-attn
```

### 6. Полезные советы
- Запускай **nvidia-smi -l 1** в отдельном окне и следи за VRAM.
- Если ошибка OOM — уменьши `--n-gpu-layers 80` или `--ctx-size 16384`.
- Для MTP (ещё быстрее) добавь `--speculative` (если твоя сборка llama.cpp поддерживает).
- После запуска проверь в Kibborg: `COMPUTER_TOOLS_URL` и порт 8080.

Кидай скрин ошибки, если что-то не запустится — сразу поправим параметры!  

Готово для Grok-кодера — копируй весь этот блок. ⚡

**KIBBORG** в деле! 
