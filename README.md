# Гибридный RAG: запуск и проверка проекта

Этот репозиторий содержит исследовательский прототип гибридной RAG-системы:

- **`ingestion-service`** — подготовка чанков, эмбеддинги E5, извлечение сущностей и отношений Ollama, запись данных в Qdrant и Neo4j;
- **`retrieval-service`** — векторный поиск, поиск по локальному графу Neo4j и гибридный поиск с RRF;
- **`benchmark`** — датасеты, подготовка PDF/Office-файлов, эталонная разметка запросов и запуск сравнения стратегий.

Всё запускается локально на Windows через PowerShell. Ни один `.env`, виртуальное окружение `.venv`, кеш модели или каталог `out/` не должен попадать в Git.

## Содержание

1. [Чего должно быть установлено](#1-чего-должно-быть-установлено)
2. [Структура репозитория](#2-структура-репозитория)
3. [Архитектура и порты](#3-архитектура-и-порты)
4. [Быстрый запуск с нуля](#4-быстрый-запуск-с-нуля)
5. [Подготовка бенчмарка из Office/PDF](#5-подготовка-бенчмарка-из-officepdf)
6. [Полная проверка системы](#6-полная-проверка-системы)
7. [Запуск бенчмарка retrieval](#7-запуск-бенчмарка-retrieval)
8. [Просмотр данных в Neo4j и Qdrant](#8-просмотр-данных-в-neo4j-и-qdrant)
9. [Тесты и проверки кода](#9-тесты-и-проверки-кода)
10. [Как остановить и очистить систему](#10-как-остановить-и-очистить-систему)
11. [Обычные проблемы](#11-обычные-проблемы)

---

## 1. Чего должно быть установлено

Перед запуском проверьте следующие инструменты.

### Windows и PowerShell

Используйте обычный PowerShell 7 или Windows PowerShell из корня репозитория.

```powershell
Set-Location "C:\путь\к\репозиторию\rag-optimization"
Get-Location
```

Если репозиторий находится в другом месте, используйте реальный полный путь. Все команды ниже считаются выполненными из корня репозитория, то есть папки, содержащей `README.md`, `benchmark`, `ingestion-service` и `retrieval-service`.

### Docker Desktop

Нужны Docker Engine и Docker Compose. Откройте Docker Desktop до выполнения `docker compose`. Docker Desktop не нужен для запуска Python или Ollama, но необходим для контейнеров Neo4j и Qdrant.

Проверка:

```powershell
docker --version
docker compose version
docker info
```

Последняя команда должна подключиться к Docker Engine без ошибки. Если Docker Desktop запущен, но команда всё ещё не отвечает, дождитесь окончания запуска Docker Desktop и проверьте снова.

### Go

В проекте используется модуль Go 1.26:

```powershell
go version
```

Если Go не найден, установите Go и откройте новый терминал.

### Python

Для проекта подходит установленная версия Python, совместимая с зависимостями. В текущем окружении проекта проверен Python 3.14. Посмотреть версию Python Launcher и доступные интерпретаторы:

```powershell
py --version
py --list
```

Если команда `py` недоступна, проверьте `python`:

```powershell
python --version
```

В командах ниже `py -m venv .venv` использует версию Python по умолчанию в системе. Если установлено несколько версий, сначала посмотрите список через `py --list`; при необходимости можно явно указать установленную версию, например `py -3.14 -m venv .venv`. Виртуальное окружение создаётся внутри `ingestion-service/.venv`, поэтому его не нужно настраивать отдельно в корне или в `retrieval-service`.

### Ollama

Для извлечения сущностей и связей нужна Ollama с моделью `qwen2.5:3b`:

```powershell
ollama --version
ollama list
```

Проверяемая модель появляется в списке после успешной загрузки.

---

## 2. Структура репозитория

```text
rag-optimization/
├── README.md                 # общий запуск проекта
├── benchmark/
│   ├── README.md             # описание форматов и оценивания
│   ├── RESULTS.md            # текущий отчёт о прогонах
│   ├── datasets/             # корпуса и вопросы датасетов
│   ├── prepare_corpus.py     # PDF/Office → Markdown для бенчмарка
│   ├── validate_dataset.py   # проверка разметки и форматов
│   └── run_retrieval.py      # живое сравнение vector/graph/hybrid
├── ingestion-service/
│   ├── corpus/               # исходный пилотный Markdown-корпус
│   ├── model-worker/
│   │   └── server.py         # HTTP-сервер обработки E5
│   ├── scripts/
│   │   ├── check_model.py    # проверка модели E5
│   │   └── check_e2e.py      # проверка пилотной связки
│   ├── main.go               # HTTP-сервис и CLI ingestion
│   ├── pipeline.go            # чанки, эмбеддинги и сущности
│   ├── rebuild.go             # пересборка индексов
│   ├── stores.go              # загрузка Qdrant и Neo4j
│   └── .venv/                # локальное Python-окружение
└── retrieval-service/
    ├── docker-compose.yml    # Qdrant и Neo4j
    ├── .env.example          # пример локального пароля
    ├── cmd/server/           # HTTP-сервер retrieval
    ├── internal/             # поиск, хранилища и объединение RRF
    └── docs/                 # контракт API и хранилищ
```

В этой схеме только один Docker Compose-файл находится в `retrieval-service/docker-compose.yml`. Отдельный `docker-compose.yml` в корне или `ingestion-service` не нужен. Compose запускает только инфраструктурные базы; Go-сервисы, Ollama и Python-worker запускаются обычными процессами.

---

## 3. Архитектура и порты

Локальный поток запроса:

```text
Пользователь
    ↓ POST /v1/search
retrieval-service :8080
    ├── POST /v1/query-context → ingestion-service :8081
    │                              └── Ollama :11434
    │                              └── E5 worker :8082
    ├── векторный поиск → Qdrant :6333
    └── графовый поиск → Neo4j :7687
```

Основные адреса:

| Компонент | Адрес | Назначение |
|---|---|---|
| Retrieval API | `http://127.0.0.1:8080` | Поиск и итоговый JSON |
| Ingestion API | `http://127.0.0.1:8081` | Эмбеддинг запроса и сущности |
| E5 worker | `http://127.0.0.1:8082` | Эмбеддинги `intfloat/multilingual-e5-small` |
| Ollama API | `http://127.0.0.1:11434` | Извлечение сущностей и связей |
| Qdrant REST | `http://127.0.0.1:6333` | Векторное хранилище |
| Qdrant gRPC | `127.0.0.1:6334` | Дополнительный протокол, обычно не нужен |
| Neo4j Bolt | `bolt://127.0.0.1:7687` | Графовая база для Go-сервисов |
| Neo4j Browser | `http://127.0.0.1:7474` | Просмотр графа вручную |

Все команды ниже работают из корня репозитория. Для каждого постоянно работающего процесса удобнее оставить отдельное окно PowerShell открытым.

---

## 4. Быстрый запуск с нуля

Ниже приведён минимальный корректный запуск пилотного корпуса.

### Шаг 1. Перейдите в корень репозитория

Откройте PowerShell:

```powershell
Set-Location "C:\Users\<имя>\OneDrive\Desktop\rag-optimization"
Get-ChildItem
```

Должны быть видны папки `benchmark`, `ingestion-service` и `retrieval-service`.

### Шаг 2. Создайте локальный `.env` для Docker

В первом терминале выполните:

```powershell
Set-Location "C:\путь\к\rag-optimization"
Set-Location .\retrieval-service

if (-not (Test-Path ".env")) {
    Copy-Item .env.example .env
}

Get-Content .env
```

Локальный пароль в примере по умолчанию:

```text
NEO4J_PASSWORD=local-dev-password
```

Файл `.env` предназначен Docker Compose и исключён из Git. Go-сервисы не читают `.env` автоматически, поэтому этот же пароль также передаётся через переменную `$env:NEO4J_PASSWORD` в соответствующих терминалах.

Не публикуйте реальный пароль или ключ в исходниках.

### Шаг 3. Запустите Docker Desktop и базы

Откройте Docker Desktop. Дождитесь, пока Engine перестанет показывать состояние запуска. Затем в том же или новом терминале выполните:

```powershell
Set-Location "C:\путь\к\rag-optimization\retrieval-service"
docker compose up -d
docker compose ps
```

Оба контейнера `qdrant` и `neo4j` должны иметь статус `Up` или `running`.

Если Compose сообщает об занятом порте `6333`, `6334`, `7474` или `7687`, остановите другой локальный процесс или контейнер, занимающий этот порт.

Проверки без входа в Docker Desktop:

```powershell
Invoke-RestMethod "http://127.0.0.1:6333/collections"
Invoke-WebRequest "http://127.0.0.1:7474" -UseBasicParsing
```

Neo4j Browser можно открыть вручную по адресу `http://127.0.0.1:7474`.

### Шаг 4. Запустите Ollama

Откройте отдельный терминал №1 и выполните:

```powershell
ollama serve
```

Оставьте этот терминал открытым. Если Ollama уже запущена приложением или фоновым процессом, сервер запускать снова не нужно.

Проверка и загрузка модели извлечения в терминале №2:

```powershell
ollama list
ollama pull qwen2.5:3b
ollama list
```

Первый запуск модели может занять больше времени. Перед пересборкой дождитесь прогрева Ollama хотя бы одним простым запросом или дайте модели несколько минут загрузиться.

### Шаг 5. Создайте и заполните виртуальное окружение Python

Откройте новый терминал №2 или используйте следующий новый терминал и выполните:

```powershell
Set-Location "C:\путь\к\rag-optimization"
Set-Location .\ingestion-service

py -m venv .venv
.\.venv\Scripts\python -m pip install --upgrade pip
.\.venv\Scripts\python -m pip install -r model-worker\requirements.txt
.\.venv\Scripts\python -m pip install -r ..\benchmark\requirements.txt
.\.venv\Scripts\python --version
```

Второй `pip install -r` нужен для подготовки PDF/Office датасетов. Если будете использовать только встроенный Markdown-корпус, benchmark-зависимости необязательны.

Не активируйте `.venv` через `Activate.ps1`, если не хотите переключать окружение: во всех командах ниже указан полный путь `.venv\Scripts\python.exe`.

Виртуальное окружение не переносится между компьютерами. Если Python обновился или путь `.venv` сломан:

```powershell
Remove-Item -Recurse -Force .venv
py -m venv .venv
.\.venv\Scripts\python -m pip install -r model-worker\requirements.txt
.\.venv\Scripts\python -m pip install -r ..\benchmark\requirements.txt
```

Удаление `.venv` безопасно для исходников и индексов. Не удаляйте другие каталоги.

### Шаг 6. Скачайте зафиксированную модель E5

Выполняйте из `ingestion-service`:

```powershell
Set-Location "C:\путь\к\rag-optimization\ingestion-service"

.\.venv\Scripts\hf download `
    intfloat/multilingual-e5-small `
    config.json model.safetensors tokenizer.json tokenizer_config.json special_tokens_map.json sentencepiece.bpe.model `
    --revision 614241f622f53c4eeff9890bdc4f31cfecc418b3
```

Модель загружается только в локальный кеш Hugging Face. Для нестандартного места кеша задайте одинаковый `HF_HOME` для скачивания и запуска worker:

```powershell
$env:HF_HOME = "C:\models-huggingface"
.\.venv\Scripts\hf download intfloat/multilingual-e5-small config.json model.safetensors tokenizer.json tokenizer_config.json special_tokens_map.json sentencepiece.bpe.model --revision 614241f622f53c4eeff9890bdc4f31cfecc418b3
```

Не включайте кеш модели в Git.

### Шаг 7. Запустите обработчик E5

Оставьте Ollama в терминале №1. В терминале №3 выполните:

```powershell
Set-Location "C:\путь\к\rag-optimization\ingestion-service"
.\.venv\Scripts\python.exe model-worker\server.py
```

Оставьте окно открытым. Worker должен слушать `127.0.0.1:8082`.

Проверка в отдельном терминале №4:

```powershell
Set-Location "C:\путь\к\rag-optimization\ingestion-service"
.\.venv\Scripts\python.exe scripts\check_model.py
```

Ожидаемый первый результат:

```text
PASS pinned E5 model: token count, 384 dimensions, normalization, prefixes, length limit
```

### Шаг 8. Остановите retrieval перед пересборкой

Пересборка безопасно запускается только тогда, когда сервис `retrieval-service` не отвечает на порту `8080`.

Не запускайте пересборку, пока открыт терминал с `go run .\cmd\server`.

Проверка порта:

```powershell
$connection = Test-NetConnection 127.0.0.1 -Port 8080 -InformationLevel Quiet
$connection
```

Если результат `False`, retrieval остановлен. Если `True`, найдите терминал retrieval и нажмите `Ctrl+C`.

**Важно:** `rebuild` удаляет содержимое коллекции Qdrant и узлы `Entity`, `Document`, `Chunk` в Neo4j, а затем индексирует заново выбранный корпус. Не используйте рабочие данные при первом запуске.

### Шаг 9. Пересоберите пилотный корпус

Откройте терминал №5:

```powershell
Set-Location "C:\путь\к\rag-optimization\ingestion-service"

$env:NEO4J_PASSWORD = "local-dev-password"
$env:MODEL_URL = "http://127.0.0.1:8082"
$env:OLLAMA_URL = "http://127.0.0.1:11434"
$env:OLLAMA_MODEL = "qwen2.5:3b"
$env:ALIASES_FILE = "corpus\aliases.json"

go run . rebuild --input corpus out\chunks.jsonl
```

Успешный итог должен выглядеть примерно так:

```text
rebuilt <количество> chunks and <количество> relations; JSONL: out\chunks.jsonl
```

При первом обращении Ollama может потребоваться до нескольких десятков секунд. Если возвращается тайм-аут, прогрейте и проверьте Ollama, дождитесь готовности модели и повторите пересборку.

### Шаг 10. Запустите ingestion-сервис

В том же терминале №5 выполните:

```powershell
go run . serve
```

Оставьте окно открытым. Оно должно слушать `127.0.0.1:8081`.

Проверка в новом терминале №6:

```powershell
Invoke-RestMethod "http://127.0.0.1:8081/healthz"
```

Ожидаемый ответ:

```json
{"status":"ok"}
```

Тогда можно вызвать обработку запроса:

```powershell
$body = @{
    query = "How did Acme work with Northstar?"
} | ConvertTo-Json

$res = Invoke-RestMethod `
    "http://127.0.0.1:8081/v1/query-context" `
    -Method Post `
    -ContentType "application/json" `
    -Body $body

$res | ConvertTo-Json -Depth 10
```

### Шаг 11. Запустите retrieval-сервис

Откройте терминал №7:

```powershell
Set-Location "C:\путь\к\rag-optimization\retrieval-service"

$env:NEO4J_PASSWORD = "local-dev-password"
$env:INGESTION_URL = "http://127.0.0.1:8081"
$env:INGESTION_TIMEOUT = "50s"
$env:QDRANT_URL = "http://127.0.0.1:6333"
$env:NEO4J_URI = "bolt://127.0.0.1:7687"

go run .\cmd\server
```

Оставьте окно открытым. Оно должно слушать `127.0.0.1:8080`.

Проверка health:

```powershell
Invoke-RestMethod "http://127.0.0.1:8080/healthz"
```

Health показывает, что HTTP-процесс запущен, но не заменяет проверку Qdrant и Neo4j.

### Шаг 12. Проверьте одну реальную поисковую выдачу

В новом терминале №8 выполните векторный поиск:

```powershell
$body = @{
    query = "How did Acme work with Northstar?"
    strategy = "vector"
    limit = 10
    graph_depth = 2
} | ConvertTo-Json

$response = Invoke-RestMethod `
    "http://127.0.0.1:8080/v1/search" `
    -Method Post `
    -ContentType "application/json" `
    -Body $body

$response | ConvertTo-Json -Depth 10
```

Графовый поиск:

```powershell
$body = @{
    query = "How did Acme work with Northstar?"
    strategy = "graph_local"
    limit = 10
    graph_depth = 2
} | ConvertTo-Json

Invoke-RestMethod `
    "http://127.0.0.1:8080/v1/search" `
    -Method Post `
    -ContentType "application/json" `
    -Body $body |
    ConvertTo-Json -Depth 10
```

Гибридный поиск:

```powershell
$body = @{
    query = "How did Acme work with Northstar?"
    strategy = "hybrid"
    limit = 10
    graph_depth = 2
} | ConvertTo-Json

Invoke-RestMethod `
    "http://127.0.0.1:8080/v1/search" `
    -Method Post `
    -ContentType "application/json" `
    -Body $body |
    ConvertTo-Json -Depth 10
```

В успешном ответе должны быть массивы `results` и `graph_facts`. Для гибридного и графового поиска проверьте факт в форме:

```text
Acme -> partnered_with -> Northstar Supplies
```

---

## 5. Подготовка бенчмарка из Office/PDF

Сейчас ingestion напрямую поддерживает только UTF-8 `.txt`, `.md` и `.markdown`. PDF, DOCX, PPTX и XLSX сначала обрабатываются benchmark-скриптами и переводятся в текстовый Markdown.

Это не изменение ingestion-сервиса. Скрипты находятся в `benchmark/` и работают с исходными датасетами без перезаписи оригиналов.

### Проверка датасетов

Из корня репозитория:

```powershell
.\ingestion-service\.venv\Scripts\python.exe .\benchmark\validate_dataset.py
```

Ожидается проверка датасетов, вопросов, псевдонимов и наличия подготовленных корпусов.

Проверка одного датасета:

```powershell
.\ingestion-service\.venv\Scripts\python.exe .\benchmark\validate_dataset.py --dataset 04_legal_contracts
```

### Подготовка одного датасета

Пример для legal:

```powershell
Set-Location "C:\путь\к\rag-optimization"

.\ingestion-service\.venv\Scripts\python.exe `
    .\benchmark\prepare_corpus.py `
    04_legal_contracts
```

Результат появится в:

```text
ingestion-service/out/benchmark/04_legal_contracts/
├── corpus/
│   ├── aliases.json
│   └── *.md
└── manifest.json
```

Скрипт не перезаписывает существующий результат: если нужно повторно подготовить датасет, сначала удалите только конкретный каталог:

```powershell
Remove-Item -Recurse -Force `
    ".\ingestion-service\out\benchmark\04_legal_contracts"
```

Не используйте рекурсивное удаление всего каталога `out`, если сохранённые результаты нужно оставить.

Проверка извлечённого текста обязательна. PDF извлекает только выделяемый текст, PDF со сканом требует OCR. PPTX не распознаёт текст на картинках. XLSX читает ячейки и формулы, но не диаграммы и макросы.

### Индексация подготовленного датасета

Сначала остановите retrieval, затем выполните:

```powershell
Set-Location "C:\путь\к\rag-optimization\ingestion-service"

$env:NEO4J_PASSWORD = "local-dev-password"
$env:MODEL_URL = "http://127.0.0.1:8082"
$env:OLLAMA_URL = "http://127.0.0.1:11434"
$env:OLLAMA_MODEL = "qwen2.5:3b"
$env:ALIASES_FILE = "out\benchmark\04_legal_contracts\corpus\aliases.json"

go run . rebuild `
    --input out\benchmark\04_legal_contracts\corpus `
    out\benchmark\04_legal_contracts\chunks.jsonl
```

После успешной пересборки запустите ingestion и retrieval заново в отдельных терминалах, как описано в шагах 10–11.

---

## 6. Полная проверка системы

`check_e2e.py` проверяет пилотный корпус `ingestion-service/corpus`, поэтому индексы должны быть пересобраны именно из этого корпуса.

При запущенных ingestion и retrieval выполните:

```powershell
Set-Location "C:\путь\к\rag-optimization\ingestion-service"

$env:INGESTION_URL = "http://127.0.0.1:8081"
$env:RETRIEVAL_URL = "http://127.0.0.1:8080"

Invoke-RestMethod "http://127.0.0.1:8081/healthz"
Invoke-RestMethod "http://127.0.0.1:8080/healthz"

.\.venv\Scripts\python.exe scripts\check_e2e.py
```

Ожидается несколько строк `PASS` для `vector`, `graph_local` и `hybrid`.

Если graph-проверка падает, сначала проверьте:

1. пересобран ли именно `ingestion-service/corpus`;
2. запущена ли Ollama с моделью `qwen2.5:3b`;
3. совпадают ли канонические имена в `corpus\aliases.json`;
4. есть ли связь в Neo4j через Cypher;
5. не осталось ли индексов от другого бенчмарк-корпуса.

Не исправляйте ожидание бенчмарка только ради прохождения проверки: сначала проверьте фактическое содержимое Neo4j и `source_chunk_ids`.

---

## 7. Запуск бенчмарка retrieval

Сначала индексируется нужный датасет. Затем запускаются одновременно база, Ollama и оба Go-сервиса.

### Подготовка и проверка датасета

```powershell
Set-Location "C:\путь\к\rag-optimization"

.\ingestion-service\.venv\Scripts\python.exe `
    .\benchmark\prepare_corpus.py `
    04_legal_contracts

.\ingestion-service\.venv\Scripts\python.exe `
    .\benchmark\validate_dataset.py `
    --dataset 04_legal_contracts
```

Для pilot можно пропустить подготовку и использовать встроенный Markdown-корпус.

### Запуск E5 worker

```powershell
Set-Location "C:\путь\к\rag-optimization\ingestion-service"
.\.venv\Scripts\python.exe model-worker\server.py
```

### Индексация prepared-корпуса

Остановите retrieval и выполните rebuild с переменными окружения и алиасами выбранного датасета:

```powershell
Set-Location "C:\путь\к\rag-optimization\ingestion-service"

$env:NEO4J_PASSWORD = "local-dev-password"
$env:MODEL_URL = "http://127.0.0.1:8082"
$env:OLLAMA_URL = "http://127.0.0.1:11434"
$env:ALIASES_FILE = "out\benchmark\04_legal_contracts\corpus\aliases.json"

go run . rebuild `
    --input out\benchmark\04_legal_contracts\corpus `
    out\benchmark\04_legal_contracts\chunks.jsonl
```

### Запуск ingestion и retrieval

`ingestion`:

```powershell
Set-Location "C:\путь\к\rag-optimization\ingestion-service"
$env:NEO4J_PASSWORD = "local-dev-password"
$env:MODEL_URL = "http://127.0.0.1:8082"
$env:OLLAMA_URL = "http://127.0.0.1:11434"
$env:ALIASES_FILE = "out\benchmark\04_legal_contracts\corpus\aliases.json"
go run . serve
```

`retrieval`:

```powershell
Set-Location "C:\путь\к\rag-optimization\retrieval-service"
$env:NEO4J_PASSWORD = "local-dev-password"
$env:INGESTION_URL = "http://127.0.0.1:8081"
$env:INGESTION_TIMEOUT = "50s"
go run .\cmd\server
```

### Запуск полного тестового бенчмарка

Из корня репозитория:

```powershell
Set-Location "C:\путь\к\rag-optimization"

.\ingestion-service\.venv\Scripts\python.exe `
    .\benchmark\run_retrieval.py `
    04_legal_contracts `
    --split test `
    --repeat 3
```

Результаты появятся в `ingestion-service/out/benchmark-results/`:

```text
*-summary.json     # сводные метрики
*.jsonl            # один JSONL-запись на случай и стратегию
```

Для тестовой выборки пилота:

```powershell
.\ingestion-service\.venv\Scripts\python.exe `
    .\benchmark\run_retrieval.py `
    01_pilot_acme `
    --split test `
    --repeat 1
```

Для всех случаев датасета:

```powershell
.\ingestion-service\.venv\Scripts\python.exe `
    .\benchmark\run_retrieval.py `
    04_legal_contracts `
    --split all `
    --repeat 3
```

Базовые unit-тесты расчёта метрик:

```powershell
.\ingestion-service\.venv\Scripts\python.exe `
    -m unittest benchmark.test_run_retrieval
```

Пояснение метрик есть в [benchmark/RESULTS.md](benchmark/RESULTS.md).

---

## 8. Просмотр данных в Neo4j и Qdrant

### Neo4j Browser

Откройте в браузере:

```text
http://127.0.0.1:7474
```

Данные входа:

- **Connect URL:** `neo4j://127.0.0.1:7687`
- **Username:** `neo4j`
- **Password:** `local-dev-password`
- **Database:** `neo4j`

Полезные запросы:

Все сущности:

```cypher
MATCH (e:Entity)
RETURN e.name AS entity
ORDER BY entity;
```

Все связи:

```cypher
MATCH (source:Entity)-[relation:RELATED_TO]->(target:Entity)
RETURN
    source.name AS source,
    relation.relation_type AS relation,
    target.name AS target,
    relation.source_chunk_ids AS source_chunk_ids
ORDER BY source, relation, target;
```

Найти Acme и соседей:

```cypher
MATCH (source:Entity)-[relation:RELATED_TO]-(target:Entity)
WHERE toLower(source.name) CONTAINS 'acme'
   OR toLower(target.name) CONTAINS 'acme'
RETURN source, relation, target;
```

Посмотреть все чанки и документы:

```cypher
MATCH (document:Document)-[:HAS_CHUNK]->(chunk:Chunk)
RETURN
    document.id AS document_id,
    chunk.id AS chunk_id,
    chunk.text AS text
ORDER BY document_id;
```

Подсчёт узлов и связей:

```cypher
MATCH (chunk:Chunk)
WITH count(chunk) AS chunks
MATCH (entity:Entity)
WITH chunks, count(entity) AS entities
MATCH ()-[relation:RELATED_TO]->()
RETURN chunks, entities, count(relation) AS relations;
```

Найти источник факта по конкретному chunk ID:

```cypher
MATCH (chunk:Chunk {id: "ВСТАВЬТЕ_ID"})
RETURN chunk.id, chunk.text;
```

Полностью очистить проектный граф вручную:

```cypher
MATCH (node)
WHERE node:Entity OR node:Chunk OR node:Document
DETACH DELETE node;
```

Эта ручная очистка не сбрасывает коллекцию Qdrant. Для согласованной пересборки используйте ingestion `rebuild`.

### Qdrant UI

Откройте:

```text
http://127.0.0.1:6333/dashboard
```

Через Dashboard можно открыть коллекции, выбрать точку и посмотреть payload. В обычном REST-вызове коллекции доступны по адресу:

```powershell
Invoke-RestMethod "http://127.0.0.1:6333/collections"
Invoke-RestMethod "http://127.0.0.1:6333/collections/documents"
```

Список точек коллекции:

```powershell
$body = @{
    limit = 10
    with_payload = $true
    with_vector = $false
} | ConvertTo-Json

Invoke-RestMethod `
    "http://127.0.0.1:6333/collections/documents/points/scroll" `
    -Method Post `
    -ContentType "application/json" `
    -Body $body |
    ConvertTo-Json -Depth 20
```

Размерность векторов должна быть `384`, метрика коллекции — `cosine`.

---

## 9. Тесты и проверки кода

Запускайте тесты из соответствующего Go-моду.

### Тесты ingestion

```powershell
Set-Location "C:\путь\к\rag-optimization\ingestion-service"
go test ./...
go vet ./...
```

Integration-тесты Go требуют работающий E5 worker на порту `8082`; Ollama подменяется тестовым сервером:

```powershell
Set-Location "C:\путь\к\rag-optimization\ingestion-service"
.\.venv\Scripts\python.exe model-worker\server.py
```

Во втором терминале:

```powershell
Set-Location "C:\путь\к\rag-optimization\ingestion-service"
go test -tags integration ./...
```

### Тесты retrieval

```powershell
Set-Location "C:\путь\к\rag-optimization\retrieval-service"
go test ./...
go vet ./...
```

У этих unit-тестов не должен быть необходим реальный Qdrant или Neo4j.

### Тесты бенчмарка Python

```powershell
Set-Location "C:\путь\к\rag-optimization"

.\ingestion-service\.venv\Scripts\python.exe `
    -m unittest benchmark.test_run_retrieval

.\ingestion-service\.venv\Scripts\python.exe `
    .\benchmark\validate_dataset.py

git diff --check
```

---

## 10. Как остановить и очистить систему

Для остановки длительных Go-сервисов и Python-worker нажмите `Ctrl+C` в их терминалах.

Остановить Ollama можно через приложение или командой:

```powershell
ollama stop qwen2.5:3b
```

Остановить контейнеры Neo4j и Qdrant:

```powershell
Set-Location "C:\путь\к\rag-optimization\retrieval-service"
docker compose stop
docker compose ps
```

Полностью удалить контейнеры и сохранить данные в Docker volumes:

```powershell
docker compose down
```

Полностью удалить контейнеры вместе с данными Neo4j и Qdrant:

```powershell
docker compose down -v
```

Команда `down -v` необратимо удаляет локальные индексы. Используйте только для полностью тестовой среды.

Удалить конкретные подготовленные результаты бенчмарка:

```powershell
Set-Location "C:\путь\к\rag-optimization"
Remove-Item -Recurse -Force `
    ".\ingestion-service\out\benchmark\04_legal_contracts"
```

Не удаляйте `.git`, `.venv` или исходные каталоги `corpus` просто ради исправления индексов.

---

## 11. Обычные проблемы

### `NEO4J_PASSWORD must be set`

Переменная не передана в Go-процесс. Выполните в том же терминале перед запуском:

```powershell
$env:NEO4J_PASSWORD = "local-dev-password"
```

Не путайте пароль Docker Compose и переменную Go: они должны совпадать.

### Docker Compose не запускается

Сначала откройте Docker Desktop и проверьте:

```powershell
docker info
docker compose version
```

Команда Compose запускается из папки с файлом:

```powershell
Set-Location "C:\путь\к\rag-optimization\retrieval-service"
docker compose up -d
```

### `retrieval-service still responds at ...; stop it before rebuilding`

Retrieval всё ещё слушает порт `8080`. Закройте терминал с `go run .\cmd\server`, затем повторите rebuild.

### Ollama отвечает по API медленно или с тайм-аутом

Прогрейте модель и проверьте состояние:

```powershell
ollama ps
ollama list
```

Обычно холодный CPU-запуск занимает больше времени. Повторите rebuild после успешного прогрева. Не заменяйте модель в CLI, если не согласовали это с кодом и датасетом.

### Ollama сообщает `out-of-memory` или `failed to allocate ... buffer`

Это нехватка памяти при запуске модели, а не ошибка формата корпуса или команды `rebuild`. Например:

```text
llama-server reported out-of-memory during startup
failed to allocate CPU buffer of size 150994944
failed to allocate buffer for kv cache
```

В этом случае llama.cpp не смог выделить примерно 144 МиБ для KV-кеша. Помимо кеша нужны память для самой модели и контекста. В нашей локальной проверке на машине с примерно 5,9 ГБ ОЗУ оставалось около 0,6 ГБ свободной памяти; контейнеры Neo4j и Qdrant вместе использовали ещё около 558 МиБ, а E5 worker тоже был запущен. На такой машине памяти может не хватить для одновременной работы Windows, Docker, E5 и Qwen 3B.

Проверить состояние перед повторной попыткой:

```powershell
$os = Get-CimInstance Win32_OperatingSystem
[pscustomobject]@{
    TotalRAM_GB = [math]::Round($os.TotalVisibleMemorySize / 1MB, 2)
    FreeRAM_GB = [math]::Round($os.FreePhysicalMemory / 1MB, 2)
}

ollama ps
docker stats --no-stream
```

Для снижения расхода памяти:

1. Закройте приложения, которые сейчас не нужны, и проверьте, что Windows использует системный файл подкачки. Подкачка может замедлить работу, но отключённая или слишком маленькая подкачка ухудшает ситуацию при нехватке памяти.
2. Проверьте `ollama ps`. Неиспользуемые загруженные модели можно выгрузить командой `ollama stop ИМЯ_МОДЕЛИ`. Не выгружайте `qwen2.5:3b`, если после этого планируете сразу проверять именно её.
3. Для CPU-компьютера уменьшите контекст Ollama до 2048 токенов, ограничьте число одновременно обслуживаемых запросов и загружаемых моделей. На Windows приложение Ollama наследует окружение при запуске: полностью завершите Ollama через значок в системном трее (Quit), затем из нового PowerShell запустите сервер с нужными переменными:

   ```powershell
   $env:OLLAMA_CONTEXT_LENGTH = "2048"
   $env:OLLAMA_NUM_PARALLEL = "1"
   $env:OLLAMA_MAX_LOADED_MODELS = "1"
   ollama serve
   ```

   Оставьте это окно открытым. Если команда сообщает, что порт `11434` уже используется, значит фоновое приложение Ollama ещё работает: завершите его через трей и повторите запуск, не поднимая два сервера.
4. В терминале пересборки ограничьте одновременные обращения ingestion к моделям:

   ```powershell
   $env:MAX_MODEL_REQUESTS = "1"
   ```

   Это сериализует запросы к worker/Ollama в ingestion-процессе. Оно не уменьшает модель, но может снизить пиковую нагрузку.
5. До `rebuild` проверьте, что Ollama действительно может загрузить модель:

   ```powershell
   ollama run qwen2.5:3b "Ответь одним словом: готов"
   ollama ps
   ```

   Если даже короткий запрос не проходит, не запускайте пересборку: сначала освободите дополнительную память, проверьте настройки подкачки или используйте компьютер с большим объёмом ОЗУ.

Понижение контекста обычно сокращает память KV-кеша. Для пилотных чанков с лимитом 384 токена 2048 токенов контекста достаточно, но вывод модели всё равно нужно проверить. Если используете другую модель, уменьшенный контекст или другие настройки, запишите это вместе с результатами: сравнение качества уже будет проводиться на изменённых условиях эксперимента.

В данном случае ошибка произошла при извлечении из первого чанка `corpus\acme-launch.md`, до очистки и загрузки Qdrant/Neo4j. Поэтому именно этот неудачный запуск не должен был изменить содержимое индексов. При другой ошибке во время загрузки хранилищ индекс может оказаться заполнен частично.

### E5 worker не отвечает на порту `8082`

Проверьте процесс:

```powershell
Get-NetTCPConnection -State Listen |
    Where-Object LocalPort -eq 8082
```

Проверьте, что Python загружал модель из локального кеша:

```powershell
Set-Location "C:\путь\к\rag-optimization\ingestion-service"
.\.venv\Scripts\python.exe scripts\check_model.py
```

Если кеш отсутствует, скачайте модель заново командой `hf download`.

### Rebuild показывает `400` из-за слишком длинного текста

Ограничение контекста чанка задаётся `CHUNK_MAX_TOKENS`; по умолчанию оно равно `384`, допустимое значение от `16` до `512`. Сам обработчик также отклоняет E5-текст длиннее 512 токенов.

### Rebuild прошёл частично

Индекс может остаться заполненным частично. Не запускайте retrieval поверх частичного индекса. Остановите retrieval, исправьте причину ошибки, затем повторите rebuild полностью.

### В Neo4j нет ожидаемой связи

Проверка графа не означает, что Ollama извлекла нужный факт. Выведите фактические рёбра:

```cypher
MATCH (source:Entity)-[relation:RELATED_TO]->(target:Entity)
RETURN source.name, relation.relation_type, target.name, relation.source_chunk_ids;
```

Сопоставьте вопрос с исходным текстом чанка, проверьте словарь псевдонимов и вручную проверьте эталонную графовую тройку.

### PDF/Office не попадают в ingestion

Это не проблема retrieval. Сначала проверьте [форматы ingestion](benchmark/README.md) и выполните benchmark-подготовку:

```powershell
.\ingestion-service\.venv\Scripts\python.exe `
    .\benchmark\prepare_corpus.py `
    04_legal_contracts
```

Официально ingestion в текущей реализации принимает только `.txt`, `.md` и `.markdown`. Остальные форматы должны быть нормализованы до запуска rebuild.
