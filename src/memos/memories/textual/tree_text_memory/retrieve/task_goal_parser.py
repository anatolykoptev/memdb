import re
import traceback

from string import Template

from memos.llms.base import BaseLLM
from memos.log import get_logger
from memos.memories.textual.tree_text_memory.retrieve.retrieval_mid_structs import ParsedTaskGoal
from memos.memories.textual.tree_text_memory.retrieve.retrieve_utils import (
    FastTokenizer,
    parse_json_result,
)
from memos.memories.textual.tree_text_memory.retrieve.utils import TASK_PARSE_PROMPT


# Temporal intent patterns (EN + RU)
_TEMPORAL_PATTERNS: list[tuple[str, str]] = [
    # last 24 hours
    (r"\b(?:today|yesterday|last\s*(?:24\s*h|night)|сегодня|вчера|за\s*(?:последние\s*)?сутки)\b", "last_24h"),
    # last 7 days
    (r"\b(?:(?:this|last|past)\s*week|last\s*(?:7|seven)\s*days|(?:на\s*)?(?:этой|прошлой)\s*неделе|за\s*(?:последн(?:юю|ие)\s*)?неделю|последни[ех]\s*7\s*дн)\b", "last_7_days"),
    # last 30 days
    (r"\b(?:(?:this|last|past)\s*month|last\s*(?:30|thirty)\s*days|(?:в\s*)?(?:этом|прошлом)\s*месяце|за\s*(?:последний\s*)?месяц|последни[ех]\s*30\s*дн)\b", "last_30_days"),
    # last 90 days / quarter
    (r"\b(?:(?:this|last|past)\s*(?:quarter|3\s*months)|last\s*(?:90|ninety)\s*days|за\s*(?:последни[ех]\s*)?(?:3\s*месяц|квартал))\b", "last_90_days"),
    # generic recent
    (r"\b(?:recently|recent|недавно|последн(?:ее|ие|ий))\b", "last_30_days"),
]
_COMPILED_TEMPORAL = [(re.compile(pat, re.IGNORECASE), scope) for pat, scope in _TEMPORAL_PATTERNS]


def detect_temporal_scope(text: str) -> str | None:
    """Detect temporal intent from query text using regex patterns (EN + RU)."""
    for pattern, scope in _COMPILED_TEMPORAL:
        if pattern.search(text):
            return scope
    return None


_SCOPE_TO_DAYS: dict[str, int] = {
    "last_24h": 1,
    "last_7_days": 7,
    "last_30_days": 30,
    "last_90_days": 90,
}


def temporal_scope_to_cutoff(scope: str | None) -> str | None:
    """Convert a temporal_scope string to an ISO-format UTC cutoff timestamp.

    Returns None if scope is None or unrecognized.
    """
    if not scope:
        return None
    from datetime import datetime, timedelta, timezone
    days = _SCOPE_TO_DAYS.get(scope)
    if days is None:
        return None
    cutoff = datetime.now(timezone.utc) - timedelta(days=days)
    return cutoff.isoformat()


logger = get_logger(__name__)


class TaskGoalParser:
    """
    Unified TaskGoalParser:
    - mode == 'fast': directly use origin task_description
    - mode == 'fine': use LLM to parse structured topic/keys/tags
    """

    def __init__(self, llm=BaseLLM):
        self.llm = llm
        self.tokenizer = FastTokenizer()
        self.retries = 1

    def parse(
        self,
        task_description: str,
        context: str = "",
        conversation: list[dict] | None = None,
        mode: str = "fast",
        **kwargs,
    ) -> ParsedTaskGoal:
        """
        Parse user input into structured semantic layers.
        Returns:
            ParsedTaskGoal: object containing topic/concept/fact levels and optional metadata
        - mode == 'fast': use jieba to split words only
        - mode == 'fine': use LLM to parse structured topic/keys/tags
        """

        if mode == "fast":
            return self._parse_fast(task_description, context=context, **kwargs)
        elif mode == "fine":
            if not self.llm:
                raise ValueError("LLM not provided for slow mode.")
            return self._parse_fine(task_description, context, conversation, **kwargs)
        else:
            raise ValueError(f"Unknown mode: {mode}")

    def _parse_fast(self, task_description: str, **kwargs) -> ParsedTaskGoal:
        """
        Fast mode: simple jieba word split.
        """
        context = kwargs.get("context", "")
        use_fast_graph = kwargs.get("use_fast_graph", False)
        temporal_scope = detect_temporal_scope(task_description)
        if temporal_scope:
            logger.info(f"[TEMPORAL] Fast-mode detected temporal_scope='{temporal_scope}' from query")
        if use_fast_graph:
            desc_tokenized = self.tokenizer.tokenize_mixed(task_description)
            return ParsedTaskGoal(
                memories=[task_description],
                keys=desc_tokenized,
                tags=desc_tokenized,
                goal_type="default",
                rephrased_query=task_description,
                internet_search=False,
                context=context,
                temporal_scope=temporal_scope,
            )
        else:
            return ParsedTaskGoal(
                memories=[task_description],
                keys=[task_description],
                tags=[],
                goal_type="default",
                rephrased_query=task_description,
                internet_search=False,
                context=context,
                temporal_scope=temporal_scope,
            )

    def _parse_fine(
        self, query: str, context: str = "", conversation: list[dict] | None = None, **kwargs
    ) -> ParsedTaskGoal:
        """
        Slow mode: LLM structured parse.
        """
        try:
            if conversation:
                conversation_prompt = "\n".join(
                    [f"{each['role']}: {each['content']}" for each in conversation]
                )
            else:
                conversation_prompt = ""
            prompt = Template(TASK_PARSE_PROMPT).substitute(
                task=query.strip(), context=context, conversation=conversation_prompt
            )
            logger.info(f"Parsing Goal... LLM input is {prompt}")
            response = self.llm.generate(messages=[{"role": "user", "content": prompt}])
            logger.info(f"Parsing Goal... LLM Response is {response}")
            return self._parse_response(response, context=context)
        except Exception:
            logger.warning(f"Fail to fine-parse query {query}: {traceback.format_exc()}")
            return self._parse_fast(query, context=context)

    def _parse_response(self, response: str, **kwargs) -> ParsedTaskGoal:
        """
        Parse LLM JSON output safely.
        """
        # Ensure at least one attempt
        attempts = max(1, getattr(self, "retries", 1))

        for attempt_times in range(attempts):
            try:
                context = kwargs.get("context", "")
                response_json = parse_json_result(response)
                if not response_json:
                    raise ValueError("Parsed JSON is empty")

                # LLM may return temporal_scope; fall back to regex detection
                llm_temporal = response_json.get("temporal_scope", None)
                if not llm_temporal:
                    rephrased = response_json.get("rephrased_instruction", "") or ""
                    llm_temporal = detect_temporal_scope(rephrased) or detect_temporal_scope(context)
                if llm_temporal:
                    logger.info(f"[TEMPORAL] Fine-mode detected temporal_scope='{llm_temporal}'")
                return ParsedTaskGoal(
                    memories=response_json.get("memories", []),
                    keys=response_json.get("keys", []),
                    tags=response_json.get("tags", []),
                    rephrased_query=response_json.get("rephrased_instruction", None),
                    internet_search=response_json.get("internet_search", False),
                    goal_type=response_json.get("goal_type", "default"),
                    context=context,
                    temporal_scope=llm_temporal,
                )
            except Exception as e:
                if attempt_times == attempts - 1:
                    raise ValueError(
                        f"Failed to parse LLM output: {e}\nRaw response:\n{response} retried: {attempt_times + 1}/{attempts}"
                    ) from e
                continue
