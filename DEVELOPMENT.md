# Development Workflow

## 🎯 Основной репозиторий для доработок

**Используйте:** `$HOME/memdb`

## 📋 Workflow для изменений

### 1. Внести изменения локально
```bash
cd $HOME/memdb
git checkout -b feature/my-feature
# Делайте изменения в коде
```

### 2. Коммит и пуш
```bash
git add .
git commit -m "feat: описание изменений"
git push origin feature/my-feature
```

### 3. CI/CD автоматически запустится
GitHub Actions выполнит все проверки:
- **16 матричных билдов:**
  - 4 ОС: ubuntu, windows, macos-14, macos-15
  - 4 версии Python: 3.10, 3.11, 3.12, 3.13

- **Проверки:**
  - ✅ Установка зависимостей
  - ✅ Сборка sdist и wheel
  - ✅ Ruff linting (`ruff check`)
  - ✅ Ruff formatting (`ruff format --check`)
  - ✅ PyTest unit tests

### 4. Обновить memdb-deploy
После пуша в GitHub:
```bash
cd $HOME/memdb-deploy/services/memdb-core
git pull origin main  # или нужную ветку
cd ../..
docker compose build --no-cache memdb-api memdb-mcp
docker compose restart memdb-api memdb-mcp
```

## 🔒 Branch Protection (main ветка)

✅ **Настроено:**
- Требуются проверки CI для Python 3.10, 3.11, 3.12, 3.13 на ubuntu-latest
- Strict mode: ветка должна быть актуальной
- Force push запрещен
- Удаление ветки запрещено

## 🧪 Локальная проверка перед коммитом

### Pre-commit hooks (опционально)
```bash
# Установить pre-commit
pip install --user pre-commit

# В директории MemDB
cd $HOME/memdb
pre-commit install

# Запустить вручную
pre-commit run --all-files
```

### Ручная проверка с Ruff
```bash
# В контейнере или локально
cd $HOME/memdb

# Проверка стиля
ruff check .

# Автоисправление
ruff check . --fix

# Проверка форматирования
ruff format --check .

# Автоформатирование
ruff format .
```

## 📊 Проверка статуса CI

```bash
cd $HOME/memdb

# Список последних запусков
gh run list --limit 10

# Статус для конкретной ветки
gh run list --branch feature/my-feature

# Просмотр логов последнего запуска
gh run view --log
```

## 🔄 Синхронизация с upstream MemDB

```bash
cd $HOME/memdb

# Добавить upstream remote (если еще нет)
git remote add upstream https://github.com/MemDBai/MemDB.git

# Получить обновления
git fetch upstream

# Слить в main
git checkout main
git merge upstream/main

# Разрешить конфликты если есть
# git add .
# git commit

# Пуш в форк
git push origin main
```

## 📁 Структура репозиториев

```
$HOME/
├── MemDB/              ⭐ ОСНОВНОЙ - все доработки здесь
│   ├── .github/workflows/ - CI/CD конфигурация
│   ├── src/memdb/         - Исходный код с патчами
│   └── tests/             - Тесты
│
├── memdb-pr-work/         🔧 Для создания PR в upstream
│   └── (ветки для PR: fix/*, feat/*)
│
└── memdb-deploy/
    ├── services/
    │   └── memdb-core/    📦 Git submodule → MemDB
    └── docker-compose.yml
```

## ✅ Гарантия качества

С этой настройкой каждый коммит в main проходит:
- ✅ 16 матричных билдов (4 ОС × 4 Python версии)
- ✅ Ruff проверки (код и форматирование)
- ✅ Unit тесты
- ✅ Проверка зависимостей

**Ваш форк теперь такой же качественный, как upstream MemDB!**

## 🚀 Quick Reference

| Задача | Команда |
|--------|---------|
| Создать ветку | `git checkout -b feature/name` |
| Запушить изменения | `git push origin feature/name` |
| Проверить CI | `gh run list --branch feature/name` |
| Обновить submodule | `cd $HOME/memdb-deploy/services/memdb-core && git pull` |
| Пересобрать контейнеры | `docker compose build --no-cache memdb-api memdb-mcp` |
| Перезапустить сервисы | `docker compose restart memdb-api memdb-mcp` |
| Проверить код Ruff | `ruff check . && ruff format --check .` |

---

**Все изменения делайте в `$HOME/memdb`**
**CI/CD гарантирует качество перед попаданием в upstream!**
