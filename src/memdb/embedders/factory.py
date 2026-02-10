from typing import Any, ClassVar

from memdb.configs.embedder import EmbedderConfigFactory
from memdb.embedders.ark import ArkEmbedder
from memdb.embedders.base import BaseEmbedder
from memdb.embedders.ollama import OllamaEmbedder
from memdb.embedders.sentence_transformer import SenTranEmbedder
from memdb.embedders.universal_api import UniversalAPIEmbedder
from memdb.memdb_tools.singleton import singleton_factory


class EmbedderFactory(BaseEmbedder):
    """Factory class for creating embedder instances."""

    backend_to_class: ClassVar[dict[str, Any]] = {
        "ollama": OllamaEmbedder,
        "sentence_transformer": SenTranEmbedder,
        "ark": ArkEmbedder,
        "universal_api": UniversalAPIEmbedder,
    }

    @classmethod
    @singleton_factory()
    def from_config(cls, config_factory: EmbedderConfigFactory) -> BaseEmbedder:
        backend = config_factory.backend
        if backend not in cls.backend_to_class:
            raise ValueError(f"Invalid backend: {backend}")
        embedder_class = cls.backend_to_class[backend]
        return embedder_class(config_factory.config)
