# Kibborg Go Engine

Это рабочий каталог продукта. Полная инструкция — в корне репозитория:

**[→ README.md](../README.md)**

Коротко:

| Файл | Зачем |
|---|---|
| `Menu.cmd` | скачать модель, настройки, сборка, старт, Chrome с отладкой |
| `Start.cmd` / `Stop.cmd` | поднять / погасить стек |
| `build.cmd` | lint + сборка (`gofmt` → `vet` → `staticcheck` → `go build`) |
| `settings.ini.example` | скопировать в `settings.ini` и заполнить |

```powershell
copy settings.ini.example settings.ini
notepad settings.ini
.\build.cmd
.\Start.cmd
```

Панель: http://127.0.0.1:8090

Модели (`models/`), `settings.ini`, `runtime/` и `*.exe` в git не входят.
