from datetime import datetime, timezone

import numpy as np

from memos.embedders.factory import OllamaEmbedder
from memos.llms.factory import AzureLLM, OllamaLLM, OpenAILLM
from memos.log import get_logger
from memos.memories.textual.item import TextualMemoryItem
from memos.memories.textual.tree_text_memory.retrieve.retrieval_mid_structs import ParsedTaskGoal


logger = get_logger(__name__)


# Mapping from temporal_scope to the recency decay coefficient.
# Higher coefficient = stronger penalty for older items.
_TEMPORAL_DECAY: dict[str | None, float] = {
    None: 0.005,          # Default: very gentle decay (~0.5% per day)
    "last_24h": 0.15,     # Aggressive: recent items strongly preferred
    "last_7_days": 0.08,
    "last_30_days": 0.03,
    "last_90_days": 0.01,
}

# How much of the final score is recency vs. similarity
_RECENCY_WEIGHT_DEFAULT = 0.15
_RECENCY_WEIGHT_TEMPORAL = 0.35  # When temporal intent is detected


def batch_cosine_similarity(
    query_vec: list[float], candidate_vecs: list[list[float]]
) -> list[float]:
    """
    Compute cosine similarity between a single query vector and multiple candidate vectors using NumPy.

    Args:
        query_vec (list[float]): The query embedding.
        candidate_vecs (list[list[float]]): A list of memory embeddings.

    Returns:
        list[float]: Cosine similarity scores for each candidate.
    """
    query = np.array(query_vec)
    candidates = np.array(candidate_vecs)

    # Normalize query and candidates
    query_norm = np.linalg.norm(query)
    candidates_norm = np.linalg.norm(candidates, axis=1)

    # Compute dot products
    dot_products = np.dot(candidates, query)

    # Avoid division by zero
    eps = 1e-10
    similarities = dot_products / (candidates_norm * query_norm + eps)

    return similarities.tolist()


class MemoryReranker:
    """
    Rank retrieved memory cards by structural priority and contextual similarity.
    """

    def __init__(self, llm: OpenAILLM | OllamaLLM | AzureLLM, embedder: OllamaEmbedder):
        self.llm = llm
        self.embedder = embedder

        # Structural priority weights
        self.level_weights = {
            "topic": 1.0,
            "concept": 1.0,
            "fact": 1.0,
        }

    @staticmethod
    def _compute_recency_score(item: TextualMemoryItem, decay: float) -> float:
        """Compute a 0-1 recency score based on created_at age in days."""
        created_at_str = getattr(item.metadata, "created_at", None)
        if not created_at_str:
            return 0.5  # neutral if unknown
        try:
            if isinstance(created_at_str, str):
                # Handle ISO format with or without timezone
                created_at_str = created_at_str.replace("Z", "+00:00")
                dt = datetime.fromisoformat(created_at_str)
            else:
                dt = created_at_str
            if dt.tzinfo is None:
                dt = dt.replace(tzinfo=timezone.utc)
            age_days = max(0.0, (datetime.now(timezone.utc) - dt).total_seconds() / 86400.0)
        except Exception:
            return 0.5
        return 1.0 / (1.0 + decay * age_days)

    def rerank(
        self,
        query: str,
        query_embedding: list[float],
        graph_results: list,
        top_k: int,
        parsed_goal: ParsedTaskGoal,
        **kwargs,
    ) -> list[tuple[TextualMemoryItem, float]]:
        """
        Rerank memory items by relevance to task.

        Args:
            query (str): Original task.
            query_embedding(list[float]): embedding of query
            graph_results (list): Combined retrieval results.
            top_k (int): Number of top results to return.
            parsed_goal (ParsedTaskGoal): Structured task representation.

        Returns:
            list(tuple): Ranked list of memory items with similarity score.
        """
        temporal_scope = getattr(parsed_goal, "temporal_scope", None)
        decay = _TEMPORAL_DECAY.get(temporal_scope, _TEMPORAL_DECAY[None])
        recency_weight = _RECENCY_WEIGHT_TEMPORAL if temporal_scope else _RECENCY_WEIGHT_DEFAULT
        sim_weight = 1.0 - recency_weight

        # Step 1: Filter out items without embeddings
        items_with_embeddings = [item for item in graph_results if item.metadata.embedding]
        embeddings = [item.metadata.embedding for item in items_with_embeddings]

        if not embeddings:
            # Use relativity from recall stage if available, otherwise default to 0.5
            return [
                (item, getattr(item.metadata, "relativity", None) or 0.5)
                for item in graph_results[:top_k]
            ]

        # Step 2: Compute cosine similarities
        similarity_scores = batch_cosine_similarity(query_embedding, embeddings)

        # Step 3: Apply structural weight boost + recency boost
        def get_weight(item: TextualMemoryItem) -> float:
            level = item.metadata.background
            return self.level_weights.get(level, 1.0)

        weighted_scores = []
        for sim, item in zip(similarity_scores, items_with_embeddings, strict=False):
            structural = sim * get_weight(item)
            recency = self._compute_recency_score(item, decay)
            score = sim_weight * structural + recency_weight * recency
            weighted_scores.append(score)

        if temporal_scope:
            logger.info(
                f"[RERANK:TEMPORAL] temporal_scope={temporal_scope}, decay={decay}, "
                f"recency_weight={recency_weight}, items={len(weighted_scores)}"
            )

        # Step 4: Sort by weighted score
        sorted_items = sorted(
            zip(items_with_embeddings, weighted_scores, strict=False),
            key=lambda pair: pair[1],
            reverse=True,
        )

        # Step 5: Return top-k items with fallback
        top_items = sorted_items[:top_k]

        if len(top_items) < top_k:
            selected_items = [item for item, _ in top_items]
            remaining = [(item, -1.0) for item in graph_results if item not in selected_items]
            top_items.extend(remaining[: top_k - len(top_items)])

        return top_items  # list of (item, score)
