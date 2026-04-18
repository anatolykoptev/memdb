#!/usr/bin/env python3
"""
MemDB Python quickstart — add memories and search them via the REST API.

Requirements: requests
Usage:        python3 main.py
"""

import json
import os
import sys

import requests


BASE_URL = os.getenv("MEMDB_URL", "http://localhost:8080")
API_KEY = os.getenv("MEMDB_API_KEY", "")  # leave empty if auth is disabled

HEADERS = {"Content-Type": "application/json"}
if API_KEY:
    HEADERS["Authorization"] = f"Bearer {API_KEY}"

USER_ID = "quickstart-user"
CUBE_ID = "quickstart-cube"

CONVERSATIONS = [
    [
        {"role": "user", "content": "I love hiking in the mountains on weekends."},
        {"role": "assistant", "content": "Great! I'll remember that you enjoy mountain hiking."},
    ],
    [
        {"role": "user", "content": "My favourite programming language is Go."},
        {"role": "assistant", "content": "Noted — Go is your preferred language."},
    ],
    [
        {"role": "user", "content": "I'm allergic to peanuts."},
        {"role": "assistant", "content": "Important — I'll keep your peanut allergy in mind."},
    ],
]


def add_memory(messages: list) -> None:
    payload = {
        "user_id": USER_ID,
        "writable_cube_ids": [CUBE_ID],
        "messages": messages,
        "async_mode": "sync",
    }
    resp = requests.post(
        f"{BASE_URL}/product/add", headers=HEADERS, data=json.dumps(payload), timeout=30
    )
    resp.raise_for_status()
    print(f"  added ({resp.status_code})")


def search_memories(query: str, top_k: int = 3) -> list:
    payload = {
        "user_id": USER_ID,
        "readable_cube_ids": [CUBE_ID],
        "query": query,
        "top_k": top_k,
        "mode": "fast",
    }
    resp = requests.post(
        f"{BASE_URL}/product/search", headers=HEADERS, data=json.dumps(payload), timeout=30
    )
    resp.raise_for_status()
    data = resp.json().get("data", {})
    return data.get("text_mem", [])


def main() -> None:
    print(f"MemDB quickstart — {BASE_URL}\n")

    print("1. Adding 3 memories...")
    for conv in CONVERSATIONS:
        add_memory(conv)

    query = "outdoor activities"
    print(f"\n2. Searching for '{query}'...")
    results = search_memories(query)
    if not results:
        print("  No results — memories may still be indexing. Try again in a moment.")
        sys.exit(0)

    print(f"\n3. Top-{len(results)} results:")
    for i, mem in enumerate(results, 1):
        text = mem.get("memory", mem.get("content", str(mem)))
        score = mem.get("score", "n/a")
        print(
            f"  [{i}] score={score:.3f}  {text}" if isinstance(score, float) else f"  [{i}] {text}"
        )

    print("\nDone.")


if __name__ == "__main__":
    main()
