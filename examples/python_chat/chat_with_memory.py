#!/usr/bin/env python3
"""End-to-end Claude API + MemDB memory tool example.

Two completely separate `tool_runner` invocations share state only through
MemDB. Session 1 stores a user preference; Session 2 — with no shared chat
history — recalls it via the MemDB-backed `memory_20250818` tool.

Run:
    pip install -r requirements.txt
    export ANTHROPIC_API_KEY=sk-ant-...
    python3 chat_with_memory.py

Optional env:
    MEMDB_URL              default http://localhost:8080
    CUBE_ID, USER_ID       default python-chat-demo
    MEMDB_SERVICE_SECRET   only when MemDB AUTH_ENABLED=true
"""

from __future__ import annotations

import os
import sys

import anthropic
import requests
from memdb_claude_memory import MemDBMemoryTool

MEMDB_URL = os.getenv("MEMDB_URL", "http://localhost:8080")
CUBE_ID = os.getenv("CUBE_ID", "python-chat-demo")
USER_ID = os.getenv("USER_ID", "python-chat-demo")
SERVICE_SECRET = os.getenv("MEMDB_SERVICE_SECRET", "")
MODEL = os.getenv("ANTHROPIC_MODEL", "claude-sonnet-4-5")


def check_memdb() -> None:
    """Fail fast if MemDB is not reachable."""
    try:
        r = requests.get(f"{MEMDB_URL}/health", timeout=5)
        r.raise_for_status()
    except Exception as exc:  # noqa: BLE001 — startup probe, surface anything
        print(f"ERROR: MemDB not reachable at {MEMDB_URL}: {exc}", file=sys.stderr)
        print(
            "Start it with: cd ~/deploy/krolik-server && docker compose up -d",
            file=sys.stderr,
        )
        sys.exit(1)
    print(f"MemDB reachable at {MEMDB_URL}\n")


def chat_once(client: anthropic.Anthropic, tool: MemDBMemoryTool, user_msg: str) -> str:
    """One independent conversation: no history carried over.

    `tool_runner` drives the tool-use loop until Claude emits a final text
    response. The adapter (`tool`) handles all `memory_save` / `memory_recall`
    / `memory_delete` calls transparently against MemDB.
    """
    runner = client.beta.messages.tool_runner(
        model=MODEL,
        betas=["context-management-2025-06-27"],
        max_tokens=1024,
        tools=[tool],
        messages=[{"role": "user", "content": user_msg}],
    )
    result = runner.until_done()
    # Final assistant message is the last text block in the last response.
    for block in reversed(result.content):
        if getattr(block, "type", None) == "text":
            return block.text
    return "(no text response)"


def main() -> None:
    check_memdb()

    if not os.getenv("ANTHROPIC_API_KEY"):
        print("ERROR: ANTHROPIC_API_KEY not set", file=sys.stderr)
        sys.exit(1)

    tool = MemDBMemoryTool(
        memdb_url=MEMDB_URL,
        cube_id=CUBE_ID,
        user_id=USER_ID,
        service_secret=SERVICE_SECRET,
    )
    client = anthropic.Anthropic()

    print("Session 1 (store preference):")
    print("Claude:", chat_once(
        client,
        tool,
        "Remember that I prefer TypeScript over Python.",
    ))

    print("\nSession 2 (fresh thread, recall):")
    print("Claude:", chat_once(
        client,
        tool,
        "What language do I prefer?",
    ))


if __name__ == "__main__":
    main()
