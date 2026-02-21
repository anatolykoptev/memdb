# Embedder — Бэкенды векторизации

Пакет `internal/embedder/` предоставляет единый интерфейс для генерации embedding-векторов.

## Интерфейс Embedder

```go
type Embedder interface {
    // Embed — батчевая векторизация текстов (хранение/документы).
    Embed(ctx context.Context, texts []string) ([][]float32, error)
    // EmbedQuery — векторизация одного поискового запроса.
    // Может применять query-специфичный префикс (e.g. "query: ").
    EmbedQuery(ctx context.Context, text string) ([]float32, error)
    // Dimension — размерность вектора.
    Dimension() int
    // Close — освобождает ресурсы.
    Close() error
}
```

`EmbedQuery` семантически отделяет поисковый запрос от хранимых документов — это позволяет применять разные префиксы для query и passage без изменения call-sites. `search/service.go` использует `EmbedQuery`, все pipeline хранения используют `Embed`.

Реализации взаимозаменяемы. Выбор — через `MEMDB_EMBEDDER_TYPE` env или `embedder.New(cfg, logger)`.

---

## Factory

**Файл:** `factory.go`

Единая точка создания embedder'а. Используется в `server.go`.

```go
emb, err := embedder.New(embedder.Config{
    Type:         "ollama",           // "onnx" | "voyage" | "ollama"
    OllamaURL:    "http://ollama:11434",
    Model:        "mxbai-embed-large",
    OllamaPrefix: "passage: ",        // doc prefix для Embed
    OllamaQuery:  "query: ",          // query prefix для EmbedQuery
}, logger)
```

`embedder.Config` — типизированный struct, все поля соответствуют env-переменным.

---

## ONNXEmbedder (локальная модель)

**Файл:** `onnx.go` (требует `cgo` build tag)

Запускает модель `multilingual-e5-large` локально через ONNX Runtime. Производит 1024-мерные векторы, идентичные Python-пайплайну.

### Инициализация ONNXEmbedder

```go
emb, err := embedder.NewONNXEmbedder(modelDir, logger)
```

Требуемые файлы в `modelDir`:

- `model_quantized.onnx` — квантизованная ONNX-модель
- `tokenizer.json` — HuggingFace tokenizer config (XLM-RoBERTa)

Системные требования:

- `/usr/lib/libonnxruntime.so` — ONNX Runtime shared library
- Потоки: 4 intra-op, 1 inter-op (инференс сериализован mutex'ом)

### Пайплайн Embed

1. **Токенизация** — HuggingFace tokenizer, `[CLS]`/`[SEP]`, truncate до 512 токенов
2. **Padding** — до `maxSeqLen` (длина самой длинной в батче), pad token ID = 1
3. **ONNX inference** (под mutex) — входные тензоры `input_ids` + `attention_mask`, выход `last_hidden_state [batch, seq, 1024]`
4. **Mean pooling** с attention mask
5. **L2-нормализация** — unit vectors для cosine similarity

### Производительность

| Сценарий | Время |
| --- | --- |
| Один текст | ~80 ms |
| Батч из N текстов | ~100 ms (один forward pass) |

### Параметры модели

| Константа | Значение |
| --- | --- |
| `e5Dim` | 1024 |
| `e5MaxLen` | 512 токенов |
| `e5PadID` | 1 (XLM-RoBERTa) |

`EmbedQuery` делегирует в `Embed` — ONNX-модель не различает query/document.

`Close()` разрушает ONNX-сессию, закрывает tokenizer, вызывает `ort.DestroyEnvironment()`.

---

## VoyageClient (облачный API)

**Файл:** `voyage.go`

HTTP-клиент к VoyageAI embedding API. Не требует ONNX и CGO.

### Инициализация VoyageClient

```go
client := embedder.NewVoyageClient(apiKey, model, logger)
```

| Параметр | По умолчанию | Описание |
| --- | --- | --- |
| `apiKey` | обязательно | API ключ VoyageAI |
| `model` | `voyage-4-lite` | Модель VoyageAI |

### Пайплайн VoyageClient

1. JSON запрос с `input_type: "query"` → POST `https://api.voyageai.com/v1/embeddings`
2. Timeout: 10 секунд
3. **Retry** с exponential backoff на 429/503/504 (3 попытки, 200ms→5s)
4. Сортировка результатов по `index`
5. Возвращает `[][]float32`, 1024-мерные векторы

`EmbedQuery` делегирует в `Embed` — VoyageAI обрабатывает query/document через `input_type`.

`Dimension()` возвращает 1024 (voyage-4-lite).

---

## OllamaClient (локальный HTTP, без CGO)

**Файл:** `ollama.go`

HTTP-клиент к Ollama `/api/embed`. Не требует ONNX Runtime, CGO или API-ключей. Поддерживает batch embedding (весь батч — один HTTP-запрос) через Ollama ≥ 0.3.6.

### Инициализация OllamaClient

```go
client := embedder.NewOllamaClient(baseURL, model, logger,
    embedder.WithOllamaDimension(1024),      // override dim (0 = auto-detect)
    embedder.WithOllamaTimeout(30*time.Second),
    embedder.WithTextPrefix("passage: "),    // doc prefix для Embed
    embedder.WithQueryPrefix("query: "),     // query prefix для EmbedQuery
    embedder.WithNormalizeL2(false),         // Ollama ≥0.3.6 нормализует сам
)
```

| Опция | По умолчанию | Описание |
| --- | --- | --- |
| `baseURL` | `http://localhost:11434` | URL Ollama сервера |
| `model` | `nomic-embed-text` | Embedding модель |
| `WithOllamaDimension(n)` | 1024 | Override dim; 0 = auto-detect из первого ответа |
| `WithOllamaTimeout(d)` | 60s | HTTP timeout |
| `WithTextPrefix(s)` | `""` | Клиентский префикс для документов (`Embed`) |
| `WithQueryPrefix(s)` | `""` | Клиентский префикс для запросов (`EmbedQuery`) |
| `WithNormalizeL2(b)` | false | Клиентская L2-нормализация (safety net для старых Ollama) |

### Пайплайн Embed / EmbedQuery

- `Embed` — применяет `WithTextPrefix`, отправляет весь батч одним POST на `/api/embed`
- `EmbedQuery` — применяет `WithQueryPrefix` (или `WithTextPrefix` если query-prefix не задан), отправляет один текст
- **Retry** с exponential backoff на 429/503/504 (3 попытки, 200ms→5s)
- **Auto-detect dim** — `Dimension()` возвращает размерность из первого реального ответа; до первого вызова — configured default (1024)
- Ollama ≥ 0.3.6 выполняет L2-нормализацию server-side

### Совместимость с ONNX (multilingual-e5-large)

Наш ONNX embedder хранит векторы **без префиксов**. Ollama-модели имеют server-side Modelfile templates, которые автоматически добавляют префиксы:

| Модель | Автопрефикс Ollama | Dim | Совместимость |
| --- | --- | --- | --- |
| `jeffh/intfloat-multilingual-e5-large` | ❌ нет | 1024 | ✅ Drop-in замена ONNX |
| `mxbai-embed-large` | `"Represent this sentence..."` | 1024 | ⚠️ Нужен stripped Modelfile |
| `nomic-embed-text` | `"search_query: "` | 768 | ❌ Другая dim |
| `bge-m3` | нет | 1024 | ✅ Совместима |

**Рекомендуемый путь для drop-in замены ONNX без re-embed:**

```bash
ollama pull jeffh/intfloat-multilingual-e5-large
MEMDB_EMBEDDER_TYPE=ollama
MEMDB_EMBEDDER_MODEL=jeffh/intfloat-multilingual-e5-large
# MEMDB_OLLAMA_PREFIX="" — без префикса, идентично ONNX
```

### Env-переменные

```bash
MEMDB_EMBEDDER_TYPE=ollama
MEMDB_OLLAMA_URL=http://ollama:11434
MEMDB_EMBEDDER_MODEL=mxbai-embed-large
MEMDB_OLLAMA_DIM=0                    # 0 = auto-detect из ответа модели
MEMDB_OLLAMA_PREFIX=                  # doc prefix (пусто = raw text)
MEMDB_OLLAMA_QUERY_PREFIX=            # query prefix (пусто = как doc prefix)
```

---

## Retry и надёжность

**Файл:** `retry.go`

Все HTTP-клиенты (Ollama, Voyage) используют `withRetry[T]` с exponential backoff:

- **3 попытки**, задержки: 200ms → 400ms → 800ms (cap 5s)
- Ретраит на: HTTP 429, 503, 502, 504, network timeout
- Не ретраит на: 400, 401, 403, 404, context cancellation

---

## Выбор бэкенда

| Сценарий | Рекомендация |
| --- | --- |
| Drop-in замена ONNX без re-embed | **Ollama** + `jeffh/intfloat-multilingual-e5-large` |
| Self-hosted, GPU нет | **Ollama** (простой деплой) или ONNX (нет сетевого latency) |
| Self-hosted, GPU есть | **Ollama** (GPU inference автоматически) |
| Минимальная задержка, нет сети | **ONNX** (in-process, ~100ms, нет HTTP overhead) |
| Cloud, нет возможности хранить модель | **VoyageAI** |
| Простота деплоя без CGO | **Ollama** (чистый HTTP, нулевые зависимости) |

```bash
# ONNX (default, in-process)
MEMDB_ONNX_MODEL_DIR=/models/multilingual-e5-large

# Ollama (рекомендуется для новых деплоев)
MEMDB_EMBEDDER_TYPE=ollama
MEMDB_OLLAMA_URL=http://ollama:11434
MEMDB_EMBEDDER_MODEL=jeffh/intfloat-multilingual-e5-large

# VoyageAI (cloud)
MEMDB_EMBEDDER_TYPE=voyage
VOYAGE_API_KEY=pa-...
MEMDB_EMBEDDER_MODEL=voyage-4-lite
```

---

## Сравнение с конкурентами

| Критерий | memdb-go | mem0 | Letta/MemGPT | Graphiti | Redis AMS |
| --- | --- | --- | --- | --- | --- |
| Batch в одном HTTP-запросе | ✅ | ✅ | ❌ N запросов | ❌ asyncio.gather | ✅ |
| In-process inference (ONNX) | ✅ | ❌ | ❌ | ❌ | ❌ |
| L2 normalize | ✅ pool.go | ✅ litellm | ❌ | ❌ | ✅ base class |
| EmbedQuery отдельно | ✅ | ✅ | ✅ | ✅ | ✅ |
| Retry с backoff | ✅ retry.go | ✅ litellm | ❌ | ❌ | ✅ |
| Auto-detect dim | ✅ OllamaClient | ✅ | ❌ | ❌ | ✅ |
| Factory pattern | ✅ factory.go | ✅ | ✅ | ✅ | ✅ |
| Кол-во бэкендов | 3 | 15+ | ~5 | 1 | ~6 |
| Язык | Go (нет GIL) | Python | Python | Python | Python |

**Наши преимущества:**

- **In-process ONNX** — нет сетевого latency, нет per-token cost, нет rate limits
- **Batch в одном запросе** — Letta делает N отдельных HTTP-запросов, мы — один
- **`EmbedQuery` + `WithQueryPrefix`** — разные префиксы для query/document без изменения call-sites
- **Auto-detect dim** — не нужно хардкодить размерность при смене модели
