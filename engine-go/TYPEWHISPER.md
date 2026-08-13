# TypeWhisper + Kibborg

Два режима одной установки TypeWhisper:

| Режим | Зачем | Как |
|---|---|---|
| **Системная диктовка** | Вайбкодинг: речь → текст в терминал, VS Code, браузер | Hotkey TypeWhisper (hold/toggle) |
| **STT для Kibborg** | Telegram voice + Web 🎙 → модель понимает речь | HTTP API `127.0.0.1:8978` |

## Установка (уже сделана на этой машине)

- App: `%LOCALAPPDATA%\TypeWhisper\TypeWhisper.exe` (бинарь: `...\current\TypeWhisper.exe`)
- User data: `%LOCALAPPDATA%\TypeWhisper-UserData\`
- Release: [typewhisper-win releases](https://github.com/TypeWhisper/typewhisper-win/releases)
- Автозапуск: Startup-ярлык

## Что уже настроено автоматически

| Параметр | Значение |
|---|---|
| HTTP API | **ON**, порт **8978** |
| Активная модель | `plugin:com.typewhisper.whisper-cpp:large-v3-turbo-q5_0` (CUDA) |
| Fallback-модель | Parakeet TDT 0.6B (sherpa-onnx, CPU) |
| Язык | auto + hints `ru`, `en` |
| Hotkey диктовки | `Ctrl+R` (из wizard) |
| Discovery | `%LOCALAPPDATA%\TypeWhisper-UserData\api-discovery.json` |

Плагины: `com.typewhisper.whisper-cpp`, `com.typewhisper.sherpa-onnx`.

Проверка:

```powershell
typewhisper status
# → status=ready, engine=whisper-cpp, model=large-v3-turbo-q5_0

Invoke-RestMethod http://127.0.0.1:8978/v1/status
```

Если API не отвечает — запусти TypeWhisper из трея/Start Menu (модель грузится ~10 с).

## Kibborg (`settings.ini`)

```ini
# пусто / auto = discover → :8978
TYPEWHISPER_URL=
TYPEWHISPER_TOKEN=

# опциональный fallback, если TypeWhisper не запущен
WHISPER_SERVER=
WHISPER_MODEL=
PORT_WHISPER=8081
```

Порядок STT:

1. TypeWhisper `POST /v1/transcribe/local-file` (ru+en hints)
2. TypeWhisper `POST /v1/transcribe` multipart WAV
3. whisper.cpp `POST /inference` (если `WHISPER_*` заданы)

## Вайбкодинг (диктовка в терминал)

1. Сфокусируй окно (Windows Terminal / Cursor / VS Code / браузер).
2. Зажми/переключи hotkey TypeWhisper.
3. Говори → текст вставляется в активное поле.

Полезные workflow в TypeWhisper:

- **Terminal / Code**: dictionary pack «developer», без лишней пунктуации
- Snippets под частые команды

## Паритет Telegram ↔ Web

- Telegram: voice/audio note → STT → «🎙 Распознал» → ответ LLM
- Web: кнопка 🎙 (hold-to-talk) или upload audio → STT → transcript в UI → ответ LLM
