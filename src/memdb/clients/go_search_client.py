"""Thin HTTP client for Go MemDB search endpoint.

Routes Python search calls through Go's /product/search instead of
hitting Qdrant/Postgres directly. This is the Python-side counterpart
of the Phase 2 migration (search pipeline → Go).
"""

from __future__ import annotations

import os
from typing import Any

import httpx

from memdb.log import get_logger
from memdb.memories.textual.item import (
    SearchedTreeNodeTextualMemoryMetadata,
    TextualMemoryItem,
)


logger = get_logger(__name__)

DEFAULT_GO_URL = "http://memdb-go:8080"
DEFAULT_TIMEOUT = 30


class GoSearchClient:
    """HTTP client that delegates search to Go's /product/search endpoint."""

    def __init__(
        self,
        base_url: str | None = None,
        timeout: int = DEFAULT_TIMEOUT,
    ) -> None:
        self.base_url = (
            base_url
            or os.getenv("MEMDB_GO_URL", DEFAULT_GO_URL)
        ).rstrip("/")
        self.timeout = timeout
        self._client = httpx.Client(
            base_url=self.base_url,
            timeout=httpx.Timeout(timeout),
        )
        logger.info("[GoSearchClient] initialized → %s (timeout=%ds)", self.base_url, timeout)

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    def search(
        self,
        query: str,
        user_name: str,
        *,
        top_k: int = 10,
        dedup: str = "no",
        relativity: float = 0.0,
        mode: str = "fast",
        include_skill: bool = True,
        include_pref: bool = True,
        include_tool: bool = False,
        include_embedding: bool = False,
        skill_top_k: int = 3,
        pref_top_k: int = 6,
        tool_top_k: int = 6,
        readable_cube_ids: list[str] | None = None,
    ) -> dict[str, Any]:
        """Call Go /product/search and return the raw ``data`` dict.

        The returned dict has the same shape as Go's ``SearchResult``:
        ``text_mem``, ``skill_mem``, ``pref_mem``, ``tool_mem``, etc.
        """
        payload: dict[str, Any] = {
            "query": query,
            "user_id": user_name,
            "top_k": top_k,
            "dedup": dedup,
            "relativity": relativity,
            "mode": mode,
            "include_skill_memory": include_skill,
            "include_preference": include_pref,
            "search_tool_memory": include_tool,
            "include_embedding": include_embedding,
            "skill_mem_top_k": skill_top_k,
            "pref_top_k": pref_top_k,
            "tool_mem_top_k": tool_top_k,
        }
        if readable_cube_ids:
            payload["readable_cube_ids"] = readable_cube_ids

        resp = self._client.post("/product/search", json=payload)
        resp.raise_for_status()
        body = resp.json()

        if body.get("code") != 200:
            raise GoSearchError(
                f"Go search returned code={body.get('code')}: {body.get('message')}"
            )

        return body.get("data", {})

    def search_formatted(
        self,
        query: str,
        user_name: str,
        *,
        top_k: int = 10,
        dedup: str = "no",
        relativity: float = 0.0,
        mode: str = "fast",
        include_skill: bool = True,
        include_pref: bool = False,
        include_tool: bool = False,
        include_embedding: bool = False,
        skill_top_k: int = 3,
        pref_top_k: int = 6,
        tool_top_k: int = 6,
        readable_cube_ids: list[str] | None = None,
    ) -> list[dict[str, Any]]:
        """Call Go search and return flat list of formatted memory dicts.

        The dicts match the shape produced by ``format_memory_item()`` —
        i.e. ``{id, ref_id, memory, metadata: {...}}``.  This is the
        drop-in replacement for ``_fast_search()`` which already returns
        ``list[dict]``.
        """
        data = self.search(
            query=query,
            user_name=user_name,
            top_k=top_k,
            dedup=dedup,
            relativity=relativity,
            mode=mode,
            include_skill=include_skill,
            include_pref=include_pref,
            include_tool=include_tool,
            include_embedding=include_embedding,
            skill_top_k=skill_top_k,
            pref_top_k=pref_top_k,
            tool_top_k=tool_top_k,
            readable_cube_ids=readable_cube_ids,
        )
        return _flatten_memory_buckets(data, keys=("text_mem", "tool_mem", "skill_mem"))

    def search_text_items(
        self,
        query: str,
        user_name: str,
        *,
        top_k: int = 10,
        dedup: str = "no",
        mode: str = "fast",
        include_skill: bool = False,
        include_pref: bool = False,
        include_tool: bool = False,
        skill_top_k: int = 3,
        pref_top_k: int = 6,
        tool_top_k: int = 6,
        readable_cube_ids: list[str] | None = None,
    ) -> list[TextualMemoryItem]:
        """Call Go search and convert to ``TextualMemoryItem`` list.

        This is the drop-in replacement for callers that expect
        ``list[TextualMemoryItem]`` (scheduler, retriever, searcher).
        """
        data = self.search(
            query=query,
            user_name=user_name,
            top_k=top_k,
            dedup=dedup,
            mode=mode,
            include_skill=include_skill,
            include_pref=include_pref,
            include_tool=include_tool,
            include_embedding=True,
            skill_top_k=skill_top_k,
            pref_top_k=pref_top_k,
            tool_top_k=tool_top_k,
            readable_cube_ids=readable_cube_ids,
        )
        return _go_data_to_text_items(data)

    def search_pref_formatted(
        self,
        query: str,
        user_name: str,
        *,
        pref_top_k: int = 6,
    ) -> list[dict[str, Any]]:
        """Search only preference memories via Go and return formatted dicts."""
        data = self.search(
            query=query,
            user_name=user_name,
            top_k=0,
            include_skill=False,
            include_pref=True,
            include_tool=False,
            pref_top_k=pref_top_k,
        )
        return _flatten_memory_buckets(data, keys=("pref_mem",))

    def close(self) -> None:
        self._client.close()


# ------------------------------------------------------------------
# Helpers
# ------------------------------------------------------------------


def _flatten_memory_buckets(
    data: dict[str, Any],
    keys: tuple[str, ...] = ("text_mem", "skill_mem", "tool_mem", "pref_mem"),
) -> list[dict[str, Any]]:
    """Extract all memory dicts from Go's bucket-based response."""
    memories: list[dict[str, Any]] = []
    for key in keys:
        for bucket in data.get(key, []):
            memories.extend(bucket.get("memories", []))
    return memories


def _go_memory_to_text_item(mem: dict[str, Any]) -> TextualMemoryItem:
    """Convert a single Go memory dict → TextualMemoryItem."""
    meta_raw = mem.get("metadata", {})
    metadata = SearchedTreeNodeTextualMemoryMetadata(
        memory_type=meta_raw.get("memory_type", "LongTermMemory"),
        relativity=meta_raw.get("relativity"),
        embedding=meta_raw.get("embedding"),
        created_at=meta_raw.get("created_at"),
        updated_at=meta_raw.get("updated_at"),
        user_id=meta_raw.get("user_id"),
        session_id=meta_raw.get("session_id"),
        status=meta_raw.get("status", "activated"),
        memory_time=meta_raw.get("memory_time"),
        sources=_parse_sources(meta_raw.get("sources")),
        custom_tags=meta_raw.get("custom_tags"),
        info=meta_raw.get("info"),
        source_doc_id=meta_raw.get("source_doc_id"),
    )
    return TextualMemoryItem(
        id=mem.get("id", meta_raw.get("id", "")),
        memory=mem.get("memory", ""),
        metadata=metadata,
    )


def _parse_sources(raw: Any) -> list | None:
    if raw is None:
        return None
    if isinstance(raw, list):
        return raw
    return None


def _go_data_to_text_items(data: dict[str, Any]) -> list[TextualMemoryItem]:
    """Convert full Go search response data → list of TextualMemoryItem."""
    items: list[TextualMemoryItem] = []
    for bucket in data.get("text_mem", []):
        for mem in bucket.get("memories", []):
            try:
                items.append(_go_memory_to_text_item(mem))
            except Exception as e:
                logger.warning("[GoSearchClient] skipping malformed memory: %s", e)
    return items


# ------------------------------------------------------------------
# Errors
# ------------------------------------------------------------------


class GoSearchError(Exception):
    """Raised when Go search endpoint returns a non-200 code."""
