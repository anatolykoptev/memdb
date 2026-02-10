from typing import Any, ClassVar

from memdb.configs.llm import LLMConfigFactory
from memdb.llms.base import BaseLLM
from memdb.llms.deepseek import DeepSeekLLM
from memdb.llms.hf import HFLLM
from memdb.llms.hf_singleton import HFSingletonLLM
from memdb.llms.ollama import OllamaLLM
from memdb.llms.openai import AzureLLM, OpenAILLM
from memdb.llms.openai_new import OpenAIResponsesLLM
from memdb.llms.qwen import QwenLLM
from memdb.llms.vllm import VLLMLLM
from memdb.memdb_tools.singleton import singleton_factory


class LLMFactory(BaseLLM):
    """Factory class for creating LLM instances."""

    backend_to_class: ClassVar[dict[str, Any]] = {
        "openai": OpenAILLM,
        "azure": AzureLLM,
        "ollama": OllamaLLM,
        "huggingface": HFLLM,
        "huggingface_singleton": HFSingletonLLM,  # Add singleton version
        "vllm": VLLMLLM,
        "qwen": QwenLLM,
        "deepseek": DeepSeekLLM,
        "openai_new": OpenAIResponsesLLM,
    }

    @classmethod
    @singleton_factory()
    def from_config(cls, config_factory: LLMConfigFactory) -> BaseLLM:
        backend = config_factory.backend
        if backend not in cls.backend_to_class:
            raise ValueError(f"Invalid backend: {backend}")
        llm_class = cls.backend_to_class[backend]
        return llm_class(config_factory.config)
