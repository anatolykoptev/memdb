import time

from typing import Any

from memos.embedders.factory import OllamaEmbedder
from memos.graph_dbs.factory import Neo4jGraphDB
from memos.llms.factory import AzureLLM, OllamaLLM, OpenAILLM
from memos.log import get_logger
from memos.memories.textual.item import TextualMemoryItem, TextualMemoryMetadata
from memos.memories.textual.tree_text_memory.retrieve.bm25_util import EnhancedBM25
from memos.memories.textual.tree_text_memory.retrieve.retrieve_utils import parse_structured_output
from memos.memories.textual.tree_text_memory.retrieve.searcher import Searcher
from memos.reranker.base import BaseReranker
from memos.templates.advanced_search_prompts import PROMPT_MAPPING
from memos.types import SearchMode


logger = get_logger(__name__)


class AdvancedSearcher(Searcher):
    def __init__(
        self,
        dispatcher_llm: OpenAILLM | OllamaLLM | AzureLLM,
        graph_store: Neo4jGraphDB,
        embedder: OllamaEmbedder,
        reranker: BaseReranker,
        bm25_retriever: EnhancedBM25 | None = None,
        internet_retriever: None = None,
        moscube: bool = False,
        search_strategy: dict | None = None,
        manual_close_internet: bool = True,
        process_llm: Any | None = None,
    ):
        super().__init__(
            dispatcher_llm=dispatcher_llm,
            graph_store=graph_store,
            embedder=embedder,
            reranker=reranker,
            bm25_retriever=bm25_retriever,
            internet_retriever=internet_retriever,
            moscube=moscube,
            search_strategy=search_strategy,
            manual_close_internet=manual_close_internet,
        )

        self.stage_retrieve_top = 3
        self.process_llm = process_llm
        self.thinking_stages = 3
        self.stage_retry_times = 2

    def load_template(self, template_name: str) -> str:
        if template_name not in PROMPT_MAPPING:
            logger.error("Prompt template is not found!")
        prompt = PROMPT_MAPPING[template_name]
        return prompt

    def build_prompt(self, template_name: str, **kwargs) -> str:
        template = self.load_template(template_name)
        if not template:
            raise FileNotFoundError(f"Prompt template `{template_name}` not found.")
        return template.format(**kwargs)

    def stage_retrieve(
        self,
        stage_id: int,
        query: str,
        previous_retrieval_phrases: list[str],
        text_memories: str,
        context: str | None = None,
    ):
        args = {
            "template_name": f"stage{stage_id}_expand_retrieve",
            "query": query,
            "previous_retrieval_phrases": previous_retrieval_phrases,
            "memories": text_memories,
        }
        if context is not None:
            args["context"] = context
        prompt = self.build_prompt(**args)

        attempt = 0
        while attempt <= max(0, self.stage_retry_times) + 1:
            try:
                llm_response = self.process_llm.generate([{"role": "user", "content": prompt}])
                result = parse_structured_output(content=llm_response)
                return (
                    result["can_answer"].lower() == "true",
                    result["reason"],
                    result["context"],
                    result["retrival_phrases"],
                )

            except Exception as e:
                attempt += 1
                time.sleep(1)
                logger.debug(
                    f"[stage_retrieve]🔁 retry {attempt}/{max(1, self.stage_retry_times) + 1} failed: {e}"
                )
        raise

    def summarize_memories(self, query: str, context: str, text_memories: str, top_k: int):
        args = {
            "template_name": "memory_summary",
            "query": query,
            "context": context,
            "memories": text_memories,
        }

        prompt = self.build_prompt(**args)

        llm_response = self.process_llm.generate([{"role": "user", "content": prompt}])
        result = parse_structured_output(content=llm_response)

        return result["context"], result["memories"]

    def deep_search(
        self,
        query: str,
        top_k: int,
        info=None,
        memory_type="All",
        search_filter: dict | None = None,
        user_name: str | None = None,
        **kwargs,
    ):
        previous_retrieval_phrases = [query]
        memories = self.search(
            query=query,
            user_name=user_name,
            top_k=top_k,
            mode=SearchMode.FAST,
            memory_type=memory_type,
            search_filter=search_filter,
            info=info,
        )

        if not memories:
            logger.warning("No memories found in initial search")
            return memories

        user_id = memories[0].metadata.user_id
        context = None
        mem_list = [mem.memory for mem in memories]
        for stage_id in range(self.thinking_stages):
            current_stage_id = stage_id + 1
            try:
                can_answer, reason, context, retrieval_phrases = self.stage_retrieve(
                    stage_id=current_stage_id,
                    query=query,
                    previous_retrieval_phrases=previous_retrieval_phrases,
                    context=context,
                    text_memories="- " + "\n- ".join(mem_list) + "\n",
                )

                logger.info(
                    "Stage %d - Found %d new retrieval phrases",
                    current_stage_id,
                    len(retrieval_phrases),
                )

                # Search for additional memories based on retrieval phrases
                for phrase in retrieval_phrases:
                    additional_memories = self.search(
                        query=phrase,
                        user_name=user_name,
                        top_k=self.stage_retrieve_top,
                        mode=SearchMode.FAST,
                        memory_type=memory_type,
                        search_filter=search_filter,
                        info=info,
                    )
                    logger.debug(
                        "Found %d additional memories for phrase: '%s'",
                        len(additional_memories),
                        phrase[:30] + "..." if len(phrase) > 30 else phrase,
                    )

                    mem_list.extend([mem.memory for mem in additional_memories])

                logger.info(
                    "After stage %d, total memories in list: %d", current_stage_id, len(mem_list)
                )

                # Summarize memories
                context, mem_list = self.summarize_memories(
                    query=query,
                    context=context,
                    text_memories="- " + "\n- ".join(mem_list) + "\n",
                    top_k=top_k,
                )
                logger.info("After summarization, memory list contains %d items", len(mem_list))

                if can_answer:
                    logger.info(
                        "Stage %d determined answer can be provided, creating enhanced memories",
                        current_stage_id,
                    )
                    enhanced_memories = []
                    for new_mem in mem_list:
                        enhanced_memories.append(
                            TextualMemoryItem(
                                memory=new_mem, metadata=TextualMemoryMetadata(user_id=user_id)
                            )
                        )
                    result_memories = enhanced_memories[:top_k]
                    logger.info(
                        "Deep search completed successfully, returning %d memories",
                        len(result_memories),
                    )
                    return result_memories
                else:
                    logger.info(
                        "Stage %d: Cannot answer yet, extending previous retrieval phrases",
                        current_stage_id,
                    )
                    previous_retrieval_phrases.extend(retrieval_phrases)
            except Exception as e:
                logger.error("Error in stage %d: %s", current_stage_id, str(e), exc_info=True)
                # Continue to next stage instead of failing completely
                continue

        return memories
