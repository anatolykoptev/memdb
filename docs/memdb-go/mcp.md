# MCP — Model Context Protocol Server

Пакет `internal/mcptools/` реализует MCP-сервер для интеграции MemDB с AI-агентами (Claude, Windsurf, Cursor и др.).

## Архитектура

MCP-сервер (`cmd/mcp-server/`) работает как отдельный процесс поверх memdb-go HTTP API. Он НЕ запускает ONNX локально — вместо этого проксирует поисковые запросы к `memdb-go /product/search`.

```
AI Agent (Claude/Cursor)
         │  MCP protocol (JSON-RPC)
         ▼
  [mcp-server процесс]
         │
         ├─── Native tools ────── Postgres (get/update/delete memory)
         │
         └─── Proxy tools ─────── memdb-go HTTP API (/product/search, /product/add, etc.)
                                          │
                                   Python backend
```

## Инструменты (Tools)

### search_memories

**Файл:** `search.go`

```
Параметры:
  query      (string, required)  — поисковый запрос
  user_id    (string)            — user ID для scoping
  cube_ids   ([]string)          — список cube IDs для поиска
  top_k      (int, default 6)    — макс. результатов на категорию
  relativity (float, default 0.85) — минимальный score
  dedup      (string, default "mmr") — режим: no, sim, mmr
```

**Реализация:**
- Проксирует запрос к `memdb-go /product/search` через HTTP
- Возвращает структуру с `text_mem`, `skill_mem`, `pref_mem`, `tool_mem`
- Read-only (помечен `ReadOnlyHint: true`)

---

### get_memory

**Файл:** `memory.go`

```
Параметры:
  cube_id   (string, required) — Memory cube ID
  memory_id (string, required) — Memory node ID (property UUID)
  user_id   (string)
```

**Реализация:**
- Напрямую через `postgres.GetMemoryByPropertyID`
- Форматирует через `search.FormatMemoryItem`
- Возвращает полные метаданные воспоминания

---

### update_memory

**Файл:** `memory.go`

```
Параметры:
  cube_id        (string, required) — Memory cube ID
  memory_id      (string, required) — Memory node ID
  memory_content (string, required) — Новый текст воспоминания
  user_id        (string)
```

**Реализация:**
- `postgres.UpdateMemoryContent` — обновляет только текст, без re-embed
- Возвращает `{"memory_id": "...", "updated": true}`

---

### delete_memory

**Файл:** `memory.go`

```
Параметры:
  cube_id   (string, required) — Memory cube ID
  memory_id (string, required) — Memory node ID
  user_id   (string)
```

**Реализация:**
- `postgres.DeleteByPropertyIDs([memory_id], userName)`
- Возвращает `{"memory_id": "...", "deleted_count": N}`

---

### delete_all_memories

**Файл:** `memory.go`

```
Параметры:
  cube_id (string, required) — Memory cube ID
  user_id (string)
```

**Реализация:**
- `postgres.DeleteAllByUser(userName)` — удаляет все узлы куба
- Возвращает `{"cube_id": "...", "deleted_count": N}`

⚠️ **Осторожно:** необратимое удаление всех воспоминаний куба.

---

### add_memory *(proxy)*

**Файл:** `proxy.go`

```
Параметры:
  user_id        (string, required)
  memory_content (string) — текст для добавления
  doc_path       (string) — путь к файлу для обработки
  mem_cube_id    (string) — target cube ID
  source         (string) — источник воспоминания
  session_id     (string)
```

Проксирует к Python backend через memdb-go.

---

### chat *(proxy)*

```
Параметры:
  user_id       (string, required)
  query         (string, required)
  mem_cube_id   (string)
  system_prompt (string)
```

Проксирует к `/product/chat/complete`.

---

### clear_chat_history *(proxy)*

```
Параметры:
  user_id (string)
```

---

### create_user *(proxy)*

```
Параметры:
  user_id   (string, required)
  role      (string) — USER или ADMIN
  user_name (string) — display name
```

---

### get_user_info *(proxy)*

```
Параметры:
  user_id (string)
```

---

### create_cube / register_cube / unregister_cube / share_cube / dump_cube *(proxy)*

Cube management операции — проксируются к Python.

---

### control_memory_scheduler *(proxy)*

```
Параметры:
  action (string) — start или stop
```

---

## Типы MCP инструментов

**Файл:** `types.go`

Все входные типы строго типизированы для корректной JSON Schema генерации:

| Тип | Инструмент |
|---|---|
| `SearchInput` | search_memories |
| `GetMemoryInput` | get_memory |
| `UpdateMemoryInput` | update_memory |
| `DeleteMemoryInput` | delete_memory |
| `DeleteAllMemoriesInput` | delete_all_memories |
| `CreateUserInput` | create_user |
| `GetUserInfoInput` | get_user_info |
| `AddMemoryProxyInput` | add_memory |
| `ChatProxyInput` | chat |
| `CreateCubeProxyInput` | create_cube |
| `RegisterCubeProxyInput` | register_cube |
| `ControlSchedulerProxyInput` | control_memory_scheduler |

---

## Proxy механизм

**Файл:** `proxy.go`

`proxyCall(ctx, memdbGoURL, path, secret, toolName, input, logger)`:
1. Сериализует `input` в JSON
2. HTTP POST к `memdbGoURL + path` с заголовком `X-Service-Secret`
3. Десериализует ответ в `TextResult{Result: any}`

Все proxy-инструменты используют один и тот же механизм — это позволяет MCP-серверу работать без прямого доступа к БД.

---

## Запуск MCP-сервера

```bash
# Сборка
cd memdb-go
go build ./cmd/mcp-server

# Stdio режим (для Claude Desktop / Windsurf)
MEMDB_GO_URL=http://localhost:8000 \
SERVICE_SECRET=... \
./mcp-server --stdio
```

### Конфигурация Claude Desktop

```json
{
  "mcpServers": {
    "memdb": {
      "command": "/path/to/mcp-server",
      "args": ["--stdio"],
      "env": {
        "MEMDB_GO_URL": "http://localhost:8000",
        "SERVICE_SECRET": "your-secret"
      }
    }
  }
}
```

---

## MCP Stdio Proxy

**Директория:** `cmd/mcp-stdio-proxy/`

Дополнительный прокси-процесс для stdio ↔ HTTP конвертации. Используется когда MCP-клиент поддерживает только stdio транспорт, но MCP-сервер работает как HTTP-сервис.

---

---

## Сравнение с конкурентами: MCP Integration

### mem0 (Python) — OpenMemory MCP

| Аспект | mem0 OpenMemory MCP | memdb-go MCP |
|---|---|---|
| Язык сервера | Python (FastAPI) | **Go** |
| Tools | add_memories, search_memories, list_memories, delete_all_memories | ✅ + get_memory, update_memory, cube management |
| Хранилище | Qdrant + SQLite | PolarDB + Qdrant + Redis |
| Docker | ✅ Одна команда | ✅ docker-compose |
| Privacy | ✅ Полностью локально | ✅ Полностью локально |
| Latency (search) | ~200ms (Python + embed) | **~100ms (ONNX + Go)** |
| Нативный transport | stdio + SSE | **stdio + HTTP** |

**Где mem0 OpenMemory сильнее:** Значительно больше готовых интеграций — официальная интеграция с Claude Desktop, Cursor, Cline, Windsurf, Continue. Активный маркетинг в сообществе.

**Цель Go-миграции:** Опубликовать готовую конфигурацию для Claude Desktop / Windsurf в README MCP-сервера. Зарегистрировать в реестрах MCP-серверов (mcp.so, Smithery).

---

### Redis Agent Memory Server — MCP

Redis agent-memory-server добавил MCP поддержку в v0.12.x. Их MCP tools:
- `memory_prompt` — контекст рабочей памяти как prompt prefix
- `search_memory_tool` — semantic search
- `manage_memory_tool` — add/update/delete

**Где redis/agent-memory-server сильнее:** `memory_prompt` tool — возвращает готовый контекст сессии как строку для injection в system prompt. Агенту не нужно самому собирать память из поисковых результатов.

**Цель Go-миграции:** Добавить `get_context` MCP tool — один вызов, возвращающий profile + act_mem + top search results как готовый контекстный блок для вставки в prompt.

---

### LangMem (Python) — LangGraph BaseStore Tools

LangMem регистрирует memory tools через `create_manage_memory_tool` и `create_search_memory_tool` — они работают напрямую через LangGraph `BaseStore`, без отдельного HTTP-сервера.

**Где LangMem сильнее:** Zero-latency память — tools вызывают BaseStore inline в агентном цикле без HTTP round-trip. Процедурная память — LLM может через tool напрямую обновлять system prompt.

**Цель Go-миграции:** Добавить `update_system_prompt` MCP tool для процедурной памяти — принимает instruction correction → сохраняет в `ProcedureMemory` → инжектируется при следующем search.

---

### Graphiti/Zep — A2A (Agent-to-Agent)

Zep предоставляет **A2A protocol** поддержку для multi-agent систем — агенты могут делиться памятью без общего MCP-сервера.

**Где Zep сильнее:** A2A multi-agent memory sharing с access control на уровне graph nodes. Наш `share_cube` proxy и your-agent MCP (`mcp5_<agent>_*`) — более грубый механизм на уровне кубов.

**Цель Go-миграции:** Расширить `share_cube` механизм — добавить fine-grained access control на уровне memory_type или тегов, не только всего куба целиком.

---

## Reembed utility

**Директория:** `cmd/reembed/`

CLI-утилита для перевекторизации существующих воспоминаний. Используется при смене embedding-модели.

```bash
./reembed --cube-id user123 --dry-run
```
