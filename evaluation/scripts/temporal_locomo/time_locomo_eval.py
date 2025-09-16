import argparse
import sys

from pathlib import Path

from locomo_ingestion import LocomoIngestor
from locomo_processor import LocomoProcessor
from modules.locomo_eval_module import LocomoEvalModelModules

from memos.log import get_logger


FILE_PATH = Path(__file__).absolute()
BASE_DIR = FILE_PATH.parent.parent.parent
sys.path.insert(0, str(BASE_DIR))  # Enable execution from any working directory

logger = get_logger(__name__)


class TimeLocomoEval(LocomoEvalModelModules):
    def __init__(self, args):
        super().__init__(args=args)

        self.locomo_ingestor = LocomoIngestor(args=args)
        self.locomo_processor = LocomoProcessor(args=args)

    def run_eval_pipeline(self):
        # Check if temporal_locomo dataset exists, if not convert it
        temporal_locomo_file = self.data_dir / "temporal_locomo" / "temporal_locomo_qa.json"
        if not temporal_locomo_file.exists():
            print(f"Temporal locomo dataset not found at {temporal_locomo_file}")
            print("Converting locomo dataset to temporal_locomo format...")
            self.convert_locomo_to_temporal_locomo(output_dir=self.data_dir / "temporal_locomo")
            print("Dataset conversion completed.")
        else:
            print(f"Temporal locomo dataset found at {temporal_locomo_file}, skipping conversion.")

        # ingestion
        if not self.ingestion_storage_dir.exists() or not any(self.ingestion_storage_dir.iterdir()):
            self.locomo_ingestor.run_ingestion()
        else:
            print(
                f"Directory {self.ingestion_storage_dir} already exists and is not empty, skipping ingestion."
            )

        # processing
        self.locomo_processor.run_locomo_processing()


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--frame",
        type=str,
        default="memos_scheduler",
        choices=["zep", "memos", "mem0", "mem0_graph", "memos_scheduler"],
        help="Specify the memory framework (zep or memos or mem0 or mem0_graph)",
    )
    parser.add_argument(
        "--version",
        type=str,
        default="v0.2.1",
        help="Version identifier for saving results (e.g., 1010)",
    )
    parser.add_argument(
        "--workers", type=int, default=10, help="Number of parallel workers to process users"
    )
    parser.add_argument(
        "--top_k", type=int, default=20, help="Number of results to retrieve in search queries"
    )
    args = parser.parse_args()

    evaluator = TimeLocomoEval(args=args)
    evaluator.run_eval_pipeline()
