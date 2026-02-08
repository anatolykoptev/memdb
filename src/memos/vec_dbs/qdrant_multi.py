"""Qdrant multi-collection adapter for preference memory.

Milvus manages multiple collections via a single client, passing collection_name
to every method call.  Qdrant binds one collection per client instance.

This adapter bridges the gap: it holds a dict of QdrantVecDB instances (one per
collection name) and routes calls by collection_name — exactly matching the
MilvusVecDB interface that PreferenceTextMemory expects.
"""

from typing import Any

from memos.configs.vec_db import QdrantMultiCollectionConfig, QdrantVecDBConfig
from memos.log import get_logger
from memos.vec_dbs.item import MilvusVecDBItem, VecDBItem
from memos.vec_dbs.qdrant import QdrantVecDB


logger = get_logger(__name__)


def _to_milvus_item(item: VecDBItem) -> MilvusVecDBItem:
    """Convert a VecDBItem to MilvusVecDBItem, pulling memory/original_text from payload."""
    payload = item.payload or {}
    return MilvusVecDBItem(
        id=item.id,
        vector=item.vector,
        payload=payload,
        score=item.score,
        memory=payload.get("memory", payload.get("preference", "")),
        original_text=payload.get("original_text", ""),
    )


def _prepare_for_qdrant(item: Any) -> VecDBItem | dict:
    """Convert MilvusVecDBItem to VecDBItem, storing memory/original_text in payload."""
    if isinstance(item, MilvusVecDBItem):
        payload = dict(item.payload) if item.payload else {}
        if item.memory:
            payload["memory"] = item.memory
        if item.original_text:
            payload["original_text"] = item.original_text
        return VecDBItem(
            id=item.id,
            vector=item.vector,
            payload=payload,
            score=item.score,
        )
    return item


def _flatten_milvus_filter(filter: dict[str, Any] | None) -> dict[str, Any] | None:
    """Flatten Milvus-style compound filters for Qdrant.

    Milvus uses ``{"and": [dict1, dict2, ...]}`` to combine filters.
    Qdrant expects a flat ``{key: scalar_value}`` dict.  This function
    merges all sub-dicts, skipping None values.
    """
    if filter is None:
        return None
    if "and" in filter and isinstance(filter["and"], list):
        flat: dict[str, Any] = {}
        for part in filter["and"]:
            if isinstance(part, dict):
                flat.update(part)
        return flat or None
    return filter


class QdrantMultiCollectionVecDB:
    """Qdrant adapter presenting the Milvus multi-collection interface.

    Every method accepts ``collection_name`` as the first positional arg,
    matching MilvusVecDB's API so that PreferenceTextMemory works unchanged.
    """

    def __init__(self, config: QdrantMultiCollectionConfig):
        self.config = config
        self._instances: dict[str, QdrantVecDB] = {}

        for name in config.collection_name:
            single_cfg = QdrantVecDBConfig(
                collection_name=name,
                vector_dimension=config.vector_dimension,
                distance_metric=config.distance_metric,
                host=config.host,
                port=config.port,
                path=config.path,
                url=config.url,
                api_key=config.api_key,
            )
            self._instances[name] = QdrantVecDB(single_cfg)
            logger.info(f"[QdrantMulti] Initialized collection '{name}'")

    def _get(self, collection_name: str) -> QdrantVecDB:
        inst = self._instances.get(collection_name)
        if inst is None:
            raise ValueError(
                f"Unknown collection '{collection_name}'. "
                f"Available: {list(self._instances.keys())}"
            )
        return inst

    # ── Collection management ─────────────────────────────────────────

    def create_collection(self) -> None:
        for inst in self._instances.values():
            inst.create_collection()

    def list_collections(self) -> list[str]:
        return list(self._instances.keys())

    def delete_collection(self, name: str) -> None:
        self._get(name).delete_collection(name)

    def collection_exists(self, name: str) -> bool:
        inst = self._instances.get(name)
        return inst.collection_exists(name) if inst else False

    # ── Search (Milvus signature) ─────────────────────────────────────

    def search(
        self,
        query_vector: list[float],
        query: str,  # ignored by Qdrant, kept for API compat
        collection_name: str,
        top_k: int,
        filter: dict[str, Any] | None = None,
        search_type: str = "dense",  # ignored — Qdrant only does dense
    ) -> list[MilvusVecDBItem]:
        flat_filter = _flatten_milvus_filter(filter)
        results = self._get(collection_name).search(query_vector, top_k, flat_filter)
        return [_to_milvus_item(r) for r in results]

    # ── CRUD (collection_name as first arg) ───────────────────────────

    def get_by_id(self, collection_name: str, id: str) -> MilvusVecDBItem | None:
        result = self._get(collection_name).get_by_id(id)
        return _to_milvus_item(result) if result else None

    def get_by_ids(self, collection_name: str, ids: list[str]) -> list[MilvusVecDBItem]:
        results = self._get(collection_name).get_by_ids(ids)
        return [_to_milvus_item(r) for r in results]

    def get_by_filter(
        self, collection_name: str, filter: dict[str, Any], scroll_limit: int = 100
    ) -> list[MilvusVecDBItem]:
        flat_filter = _flatten_milvus_filter(filter) or {}
        results = self._get(collection_name).get_by_filter(flat_filter, scroll_limit)
        return [_to_milvus_item(r) for r in results]

    def get_all(self, collection_name: str, scroll_limit: int = 100) -> list[MilvusVecDBItem]:
        results = self._get(collection_name).get_all(scroll_limit)
        return [_to_milvus_item(r) for r in results]

    def count(self, collection_name: str, filter: dict[str, Any] | None = None) -> int:
        return self._get(collection_name).count(filter)

    def add(self, collection_name: str, data: list[VecDBItem | MilvusVecDBItem | dict[str, Any]]) -> None:
        prepared = [_prepare_for_qdrant(item) for item in data]
        self._get(collection_name).add(prepared)

    def update(self, collection_name: str, id: str, data: VecDBItem | MilvusVecDBItem | dict[str, Any]) -> None:
        prepared = _prepare_for_qdrant(data)
        self._get(collection_name).update(id, prepared)

    def upsert(self, collection_name: str, data: list[VecDBItem | MilvusVecDBItem | dict[str, Any]]) -> None:
        prepared = [_prepare_for_qdrant(item) for item in data]
        self._get(collection_name).upsert(prepared)

    def delete(self, collection_name: str, ids: list[str]) -> None:
        self._get(collection_name).delete(ids)

    def delete_by_filter(self, collection_name: str, filter: dict[str, Any]) -> None:
        """Delete items matching filter — implemented via scroll + delete."""
        inst = self._get(collection_name)
        flat_filter = _flatten_milvus_filter(filter) or {}
        items = inst.get_by_filter(flat_filter)
        if items:
            inst.delete([item.id for item in items])
            logger.info(
                f"[QdrantMulti] Deleted {len(items)} items from '{collection_name}' by filter"
            )

    def ensure_payload_indexes(self, fields: list[str]) -> None:
        for inst in self._instances.values():
            inst.ensure_payload_indexes(fields)
