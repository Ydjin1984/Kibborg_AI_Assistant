**✅ ПОЛНЫЙ СТЕК НАСТРОЙКИ ПОД ТВОЁ ЖЕЛЕЗО (2× RTX 3060 12 ГБ + 176 ГБ RAM + Xeon)** ⚙️

### 1. Рекомендуемая модель (по умолчанию)
**Qwen3.6-35B-A3B** — Unsloth `Qwen3.6-35B-A3B-UD-IQ4_XS.gguf` (~19.7 ГБ) + `mmproj-BF16.gguf`.
Агент + нативное зрение, ~3B активных параметров. На обе 3060 через tensor-split.

### 2. Прямые ссылки

- [модель UD-IQ4_XS](https://huggingface.co/unsloth/Qwen3.6-35B-A3B-GGUF/resolve/main/Qwen3.6-35B-A3B-UD-IQ4_XS.gguf?download=true)
- [mmproj-BF16](https://huggingface.co/unsloth/Qwen3.6-35B-A3B-GGUF/resolve/main/mmproj-BF16.gguf?download=true)

### 3. Команда для скачивания
```bash
pip install -U "huggingface_hub[cli]"

hf download unsloth/Qwen3.6-35B-A3B-GGUF \
  Qwen3.6-35B-A3B-UD-IQ4_XS.gguf mmproj-BF16.gguf \
  --local-dir ./models/brain/Qwen3.6-35B-A3B
```

### 4. Полная команда запуска llama-server (оптимизировано под dual 3060)

```bash
./llama-server \
  -m Qwen3.6-35B-A3B-UD-IQ4_XS.gguf \
  --mmproj mmproj-BF16.gguf \
  --ctx-size 32768 \
  --n-gpu-layers 99 \
  --tensor-split 0.35,0.65 \
  --main-gpu 0 \
  --cache-type-k q8_0 \
  --cache-type-v q8_0 \
  --flash-attn \
  --threads 28 \
  --port 8083 \
  --no-mmap \
  --reasoning off \
  --reasoning-budget 0 \
  --temp 0.7 \
  --top-p 0.95 \
  --repeat-penalty 1.1
```

GPU 0 обычно с монитором — поэтому 0.35/0.65, больше весов на свободную GPU 1.

### 5. Полезные советы
- Запускай **nvidia-smi -l 1** в отдельном окне и следи за VRAM.
- Если ошибка OOM — уменьши `--n-gpu-layers 80` или `--ctx-size 16384`.
- Рабочий максимум контекста на двух 12 ГБ: 32K стабильно, 64K с KV q4_0.
- После смены модели: Stop → Start (или Меню → 6, затем 5).

Кидай скрин ошибки, если что-то не запустится — сразу поправим параметры!

Готово для Grok-кодера — копируй весь этот блок. ⚡

**KIBBORG** в деле!
