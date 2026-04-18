# Python Quickstart

Adds 3 memories to MemDB and searches them using plain HTTP (`requests`).
No SDK — every call is a visible `POST` to the REST API.

## Prerequisites

- Python 3.9+
- MemDB running: `cd ~/deploy/krolik-server && docker compose up -d`

## Run

```bash
pip install -r requirements.txt
python3 main.py
```

With auth enabled:

```bash
MEMDB_API_KEY=your-key python3 main.py
```

Custom URL:

```bash
MEMDB_URL=http://my-server:8080 python3 main.py
```

## Expected output

```
MemDB quickstart — http://localhost:8080

1. Adding 3 memories...
  added (200)
  added (200)
  added (200)

2. Searching for 'outdoor activities'...

3. Top-1 results:
  [1] score=0.921  User enjoys mountain hiking on weekends.

Done.
```

## API endpoints used

| Endpoint | Purpose |
|---|---|
| `POST /product/add` | Store a conversation as memories |
| `POST /product/search` | Semantic search over stored memories |
