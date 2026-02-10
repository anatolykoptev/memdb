from typing import Any, ClassVar

from memdb.configs.memory import MemoryConfigFactory
from memdb.memories.activation.base import BaseActMemory
from memdb.memories.activation.kv import KVCacheMemory
from memdb.memories.activation.vllmkv import VLLMKVCacheMemory
from memdb.memories.base import BaseMemory
from memdb.memories.parametric.base import BaseParaMemory
from memdb.memories.parametric.lora import LoRAMemory
from memdb.memories.textual.base import BaseTextMemory
from memdb.memories.textual.general import GeneralTextMemory
from memdb.memories.textual.naive import NaiveTextMemory
from memdb.memories.textual.preference import PreferenceTextMemory
from memdb.memories.textual.simple_preference import SimplePreferenceTextMemory
from memdb.memories.textual.simple_tree import SimpleTreeTextMemory
from memdb.memories.textual.tree import TreeTextMemory


class MemoryFactory(BaseMemory):
    """Factory class for creating memory instances."""

    backend_to_class: ClassVar[dict[str, Any]] = {
        "naive_text": NaiveTextMemory,
        "general_text": GeneralTextMemory,
        "tree_text": TreeTextMemory,
        "simple_tree_text": SimpleTreeTextMemory,
        "pref_text": PreferenceTextMemory,
        "simple_pref_text": SimplePreferenceTextMemory,
        "kv_cache": KVCacheMemory,
        "vllm_kv_cache": VLLMKVCacheMemory,
        "lora": LoRAMemory,
    }

    @classmethod
    def from_config(
        cls, config_factory: MemoryConfigFactory
    ) -> BaseTextMemory | BaseActMemory | BaseParaMemory:
        backend = config_factory.backend
        if backend not in cls.backend_to_class:
            raise ValueError(f"Invalid backend: {backend}")
        memory_class = cls.backend_to_class[backend]
        return memory_class(config_factory.config)
