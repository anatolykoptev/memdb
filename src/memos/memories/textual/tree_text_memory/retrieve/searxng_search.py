"""SearXNG Search API retriever for tree text memory."""

import logging
import uuid
from datetime import datetime

import requests

from memos.embedders.base import BaseEmbedder
from memos.memories.textual.item import (
    SourceMessage,
    TextualMemoryItem,
    TreeNodeTextualMemoryMetadata,
)

logger = logging.getLogger(__name__)


class SearxngSearchAPI:
    """SearXNG Search API Client"""

    def __init__(self, base_url: str, max_results: int = 20):
        """
        Initialize SearXNG Search API client.

        Args:
            base_url: SearXNG instance URL (e.g. http://searxng:8080)
            max_results: Maximum number of results to retrieve
        """
        self.base_url = base_url.rstrip("/")
        self.max_results = max_results

    def search(self, query: str, max_results: int | None = None) -> list[dict]:
        """
        Execute search request.

        Args:
            query: Search query
            max_results: Maximum number of results

        Returns:
            List of search result dicts with title, url, content keys
        """
        if max_results is None:
            max_results = self.max_results

        params = {
            "q": query,
            "format": "json",
            "categories": "general",
        }

        try:
            response = requests.get(
                f"{self.base_url}/search", params=params, timeout=10
            )
            response.raise_for_status()
            data = response.json()
            return data.get("results", [])[:max_results]
        except requests.exceptions.RequestException as e:
            logger.error(f"SearXNG search request failed: {e}")
            return []


class SearxngSearchRetriever:
    """SearXNG retriever that converts search results into TextualMemoryItem objects"""

    def __init__(
        self,
        base_url: str,
        embedder: BaseEmbedder,
        max_results: int = 20,
    ):
        """
        Initialize SearXNG Search retriever.

        Args:
            base_url: SearXNG instance URL
            embedder: Embedder instance for generating embeddings
            max_results: Maximum number of results to retrieve
        """
        self.searxng_api = SearxngSearchAPI(base_url, max_results=max_results)
        self.embedder = embedder

    def retrieve_from_internet(
        self, query: str, top_k: int = 10, parsed_goal=None, info=None, mode="fast"
    ) -> list[TextualMemoryItem]:
        """
        Retrieve information from SearXNG and convert to TextualMemoryItem format.

        Args:
            query: Search query
            top_k: Number of results to return
            parsed_goal: Parsed task goal (optional)
            info: Record of memory consumption

        Returns:
            List of TextualMemoryItem
        """
        if not info:
            info = {"user_id": "", "session_id": ""}

        search_results = self.searxng_api.search(query, max_results=top_k)

        memory_items = []
        for result in search_results:
            title = result.get("title", "")
            snippet = result.get("content", "")
            link = result.get("url", "")

            memory_content = f"Title: {title}\nSummary: {snippet}\nSource: {link}"

            tags = ["web_search"]
            if parsed_goal:
                if hasattr(parsed_goal, "topic") and parsed_goal.topic:
                    tags.append(parsed_goal.topic)
                if hasattr(parsed_goal, "concept") and parsed_goal.concept:
                    tags.append(parsed_goal.concept)

            metadata = TreeNodeTextualMemoryMetadata(
                user_id=info.get("user_id", ""),
                session_id=info.get("session_id", ""),
                status="activated",
                type="fact",
                memory_time=datetime.now().strftime("%Y-%m-%d"),
                source="web",
                confidence=85.0,
                tags=tags,
                visibility="public",
                memory_type="LongTermMemory",
                key=title,
                sources=[SourceMessage(type="web", url=link)] if link else [],
                embedding=self.embedder.embed([memory_content])[0],
                created_at=datetime.now().isoformat(),
                usage=[],
                background=f"SearXNG search result",
            )

            memory_item = TextualMemoryItem(
                id=str(uuid.uuid4()), memory=memory_content, metadata=metadata
            )
            memory_items.append(memory_item)

        return memory_items
