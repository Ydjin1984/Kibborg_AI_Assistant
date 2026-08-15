# Browser Agent — спецификация (Go-порт ТЗ)

Это адаптация исходного ТЗ «Browser Agent для локального ИИ» под наш стек **Go**
(а не Python/Playwright из оригинала). Файл — живая спецификация рядом с кодом,
по образцу `GO_REFERENCE.md`.

## Что меняется при переносе Python → Go

| Оригинал (ТЗ, Python)        | Наш стек (Go)                                            |
|------------------------------|---------------------------------------------------------|
| Playwright                   | **chromedp** (`github.com/chromedp/chromedp`) поверх CDP |
| `cdp_client.py`              | не нужен отдельно — chromedp *и есть* CDP-клиент         |
| BeautifulSoup / lxml         | **goquery** (`github.com/PuerkitoBio/goquery`)          |
| pandas DataFrame             | «records» — `[]map[string]any` (Go-нативный эквивалент) |
| `*.py` модули                | `*.go` файлы в пакете `browser`                         |

Подключение к Chrome — строго по ТЗ: к **уже запущенному** Chrome через
`--remote-debugging-port=9222` по Chrome DevTools Protocol. Новый браузер не
запускаем; работаем с уже открытыми вкладками. Источник данных — **DOM и сеть**,
не OCR/скриншоты (скриншоты — только по явному запросу пользователя).

## Структура пакета (Go-аналог дерева из ТЗ)

```
browser/
├── session.go     # browser_manager.py + cdp_client.py — коннект к :9222, вкладки, сетевой захват
├── controller.go  # page_controller.py — навигация и действия (клик, ввод, формы, скролл, файлы)
├── dom.go         # dom_parser.py — чтение DOM, ссылки, картинки, таблицы, формы, storage, cookies
├── network.go     # network_monitor.py — захват XHR/Fetch/Network/WebSocket из CDP-событий
├── cloner.go      # website_cloner.py — клонирование сайта (HTML/CSS/JS/img/шрифты/svg), перелинковка
├── screenshot.go  # screenshot_service.py — скриншот страницы/элемента (только по запросу)
├── export.go      # JSON / CSV / Markdown / records — экспорт извлечённых данных
├── tools.go       # tools.py — OpenAI-описания инструментов + диспетчер вызовов
└── schema.go      # schemas.py — типы аргументов/результатов и захваченных данных
```

## Tool Calling

Каждая возможность — отдельный инструмент в OpenAI-формате (`type: function`),
который локальная LLM вызывает через `tool_calls` (llama-server). Агентный цикл
(`agent.go` в корне) выполняет инструмент, кладёт результат как `role: tool` и
повторяет, пока модель не даст финальный текстовый ответ.

Инструменты (имена совпадают с ТЗ, плюс веб-поиск):

- Поиск: `web_search` (Яндекс + Google через Chrome; HTTP: Bing, DuckDuckGo)
- Навигация/вкладки: `open_url`, `close_page`, `switch_tab`, `list_tabs`
- Действия: `click_element`, `type_text`, `scroll_page`, `select_option`,
  `submit_form`, `upload_file`, `drag_element`
- DOM/данные: `analyze_dom`, `get_html`, `get_text`, `extract_links`,
  `extract_images`, `extract_table`, `extract_forms`, `extract_json`,
  `get_storage`, `get_cookies`
- Сеть: `get_network_requests`, `get_websocket_messages`, `get_response_body`
- Прочее: `download_file`, `download_video` (YouTube/Instagram/TikTok… через yt-dlp), `clone_website`, `capture_screenshot`

## Результат

Агент умеет: анализировать любую открытую вкладку, тянуть данные напрямую из DOM и
Network, выполнять действия в браузере, сам выбирать инструменты через Tool Calling,
работать поверх CDP (chromedp), использовать скриншоты только по запросу,
экспортировать в JSON/CSV/Markdown и клонировать сайт в локальную директорию.
</content>
</invoke>
