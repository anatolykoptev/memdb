from typing import TYPE_CHECKING

from memdb.configs.memory import TreeTextMemoryConfig
from memdb.embedders.base import BaseEmbedder
from memdb.graph_dbs.base import BaseGraphDB
from memdb.llms.base import BaseLLM
from memdb.log import get_logger
from memdb.mem_reader.base import BaseMemReader
from memdb.memories.textual.tree import TreeTextMemory
from memdb.memories.textual.tree_text_memory.organize.manager import MemoryManager
from memdb.memories.textual.tree_text_memory.retrieve.bm25_util import EnhancedBM25
from memdb.memories.textual.tree_text_memory.retrieve.retrieve_utils import FastTokenizer
from memdb.reranker.base import BaseReranker


if TYPE_CHECKING:
    from memdb.llms.factory import AzureLLM, OllamaLLM, OpenAILLM


logger = get_logger(__name__)


class SimpleTreeTextMemory(TreeTextMemory):
    """General textual memory implementation for storing and retrieving memories."""

    def __init__(
        self,
        llm: BaseLLM,
        embedder: BaseEmbedder,
        mem_reader: BaseMemReader,
        graph_db: BaseGraphDB,
        reranker: BaseReranker,
        memory_manager: MemoryManager,
        config: TreeTextMemoryConfig,
        internet_retriever: None = None,
        is_reorganize: bool = False,
        tokenizer: FastTokenizer | None = None,
        include_embedding: bool = False,
    ):
        """Initialize memory with the given configuration."""
        self.config: TreeTextMemoryConfig = config
        self.mode = self.config.mode
        logger.info(f"Tree mode is {self.mode}")

        self.extractor_llm: OpenAILLM | OllamaLLM | AzureLLM = llm
        self.dispatcher_llm: OpenAILLM | OllamaLLM | AzureLLM = llm
        self.embedder: BaseEmbedder = embedder
        self.graph_store: BaseGraphDB = graph_db
        self.search_strategy = config.search_strategy
        self.bm25_retriever = (
            EnhancedBM25()
            if self.search_strategy and self.search_strategy.get("bm25", False)
            else None
        )
        self.tokenizer = tokenizer
        self.reranker = reranker
        self.memory_manager: MemoryManager = memory_manager
        # Create internet retriever if configured
        self.internet_retriever = None
        if config.internet_retriever is not None:
            self.internet_retriever = internet_retriever
            logger.info(
                f"Internet retriever initialized with backend: {config.internet_retriever.backend}"
            )
        else:
            logger.info("No internet retriever configured")
        self.include_embedding = include_embedding
