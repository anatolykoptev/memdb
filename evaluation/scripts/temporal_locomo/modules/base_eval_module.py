import json
import os

from collections import defaultdict
from pathlib import Path
from threading import Lock

import pandas as pd

from dotenv import load_dotenv

from memos.configs.mem_scheduler import AuthConfig
from memos.log import get_logger

from .constants import (
    BASE_DIR,
)
from .prompts import (
    CUSTOM_INSTRUCTIONS,
)


logger = get_logger(__name__)


class BaseEvalModule:
    def __init__(self, args):
        # hyper-parameters
        self.args = args
        self.frame = self.args.frame
        self.version = self.args.version
        self.workers = self.args.workers
        self.top_k = self.args.top_k

        # attributes
        self.custom_instructions = CUSTOM_INSTRUCTIONS
        self.data_dir = Path(f"{BASE_DIR}/data")
        self.locomo_df = pd.read_json(f"{self.data_dir}/locomo/locomo10.json")

        # Load temporal_locomo dataset if it exists
        self.temporal_locomo_data = None
        temporal_locomo_file = self.data_dir / "temporal_locomo" / "temporal_locomo_qa.json"
        if temporal_locomo_file.exists():
            with open(temporal_locomo_file, encoding="utf-8") as f:
                self.temporal_locomo_data = json.load(f)
            logger.info(
                f"Loaded temporal_locomo dataset with {len(self.temporal_locomo_data)} conversations"
            )
        else:
            logger.warning(f"Temporal locomo dataset not found at {temporal_locomo_file}")
        self.result_dir = Path(f"{BASE_DIR}/results/temporal_locomo/{self.frame}-{self.version}/")
        self.result_dir.mkdir(parents=True, exist_ok=True)

        self.search_path = self.result_dir / f"{self.frame}_locomo_search_results.json"
        self.response_path = self.result_dir / Path(f"{self.frame}_locomo_responses.json")
        self.ingestion_storage_dir = self.result_dir / "storages"
        self.mos_config_path = Path(f"{BASE_DIR}/configs-example/mos_w_scheduler_config.json")
        self.mem_cube_config_path = Path(f"{BASE_DIR}/configs-example/mem_cube_config.json")
        self.openai_api_key = os.getenv("CHAT_MODEL_API_KEY")
        self.openai_base_url = os.getenv("CHAT_MODEL_BASE_URL")
        self.openai_chat_model = os.getenv("CHAT_MODEL")

        auth_config_path = Path(f"{BASE_DIR}/scripts/temporal_locomo/eval_auth.json")
        if auth_config_path.exists():
            auth_config = AuthConfig.from_local_config(config_path=auth_config_path)

            self.mos_config_data = json.load(self.mos_config_path.open("r", encoding="utf-8"))
            self.mem_cube_config_data = json.load(
                self.mem_cube_config_path.open("r", encoding="utf-8")
            )

            # Update LLM authentication information in MOS configuration using dictionary assignment
            self.mos_config_data["mem_reader"]["config"]["llm"]["config"]["api_key"] = (
                auth_config.openai.api_key
            )
            self.mos_config_data["mem_reader"]["config"]["llm"]["config"]["api_base"] = (
                auth_config.openai.base_url
            )

            # Update graph database authentication information in memory cube configuration using dictionary assignment
            self.mem_cube_config_data["text_mem"]["config"]["graph_db"]["config"]["uri"] = (
                auth_config.graph_db.uri
            )
            self.mem_cube_config_data["text_mem"]["config"]["graph_db"]["config"]["user"] = (
                auth_config.graph_db.user
            )
            self.mem_cube_config_data["text_mem"]["config"]["graph_db"]["config"]["password"] = (
                auth_config.graph_db.password
            )
            self.mem_cube_config_data["text_mem"]["config"]["graph_db"]["config"]["db_name"] = (
                auth_config.graph_db.db_name
            )
            self.mem_cube_config_data["text_mem"]["config"]["graph_db"]["config"]["auto_create"] = (
                auth_config.graph_db.auto_create
            )

            self.openai_api_key = auth_config.openai.api_key
            self.openai_base_url = auth_config.openai.base_url
            self.openai_chat_model = auth_config.openai.default_model
        else:
            print("Please referring to configs-example to provide valid configs.")
            exit()

        # Logger initialization
        self.logger = logger

        # Statistics tracking with thread safety
        self.stats = {self.frame: {self.version: defaultdict(dict)}}
        self.stats[self.frame][self.version]["response_stats"] = defaultdict(dict)
        self.stats[self.frame][self.version]["response_stats"]["response_failure"] = 0
        self.stats[self.frame][self.version]["response_stats"]["response_count"] = 0

        self.stats[self.frame][self.version]["memory_stats"] = defaultdict(dict)
        self.stats[self.frame][self.version]["memory_stats"]["total_queries"] = 0
        self.stats[self.frame][self.version]["memory_stats"]["can_answer_count"] = 0
        self.stats[self.frame][self.version]["memory_stats"]["cannot_answer_count"] = 0
        self.stats[self.frame][self.version]["memory_stats"]["answer_hit_rate"] = 0.0

        # Initialize detailed memory stats for comprehensive analysis
        self.stats[self.frame][self.version]["detailed_memory_stats"] = {
            "query_analysis": [],  # List of detailed query analysis
            "conversation_stats": defaultdict(dict),  # Per conversation statistics
            "working_memory_snapshots": defaultdict(list),  # Working memory states over time
            "query_history": defaultdict(list),  # Historical queries per conversation
            "memory_retrieval_patterns": defaultdict(list),  # Memory retrieval patterns
        }

        # Initialize memory history for tracking retrieval results
        self.memory_history = defaultdict(list)

        self.stats_lock = Lock()
        self.scheduler_flag = True
        self.stats_dir = self.result_dir / "stats"
        self.stats_dir.mkdir(parents=True, exist_ok=True)  # Ensure the directory exists
        self.stats_path = self.stats_dir / "stats.txt"

        load_dotenv()
