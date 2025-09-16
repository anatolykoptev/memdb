import json
import os
import sys
import traceback

from collections import defaultdict
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path
from time import time

from dotenv import load_dotenv
from modules.locomo_eval_module import LocomoEvalModelModules
from modules.prompts import (
    SEARCH_PROMPT_MEM0,
    SEARCH_PROMPT_MEM0_GRAPH,
    SEARCH_PROMPT_MEMOS,
    SEARCH_PROMPT_ZEP,
)
from openai import OpenAI
from tqdm import tqdm

from memos.log import get_logger


FILE_PATH = Path(__file__).absolute()
BASE_DIR = FILE_PATH.parent.parent.parent
sys.path.insert(0, str(BASE_DIR))  # Enable execution from any working directory

logger = get_logger(__name__)


class LocomoProcessor(LocomoEvalModelModules):
    """
    A class for handling conversational memory management across different memory frameworks.
    Supports multiple memory backends (zep, mem0, memos, etc.) for searching and retrieving
    relevant context to generate conversational responses.
    """

    def __init__(self, args):
        """Initialize the LocomoChatter with path configurations and templates"""
        super().__init__(args=args)

        # Template definitions for different memory frameworks
        self.search_template_zep = SEARCH_PROMPT_ZEP

        self.search_template_mem0 = SEARCH_PROMPT_MEM0

        self.search_template_mem0_graph = SEARCH_PROMPT_MEM0_GRAPH

        self.search_template_memos = SEARCH_PROMPT_MEMOS

        self.processed_data_dir = self.result_dir / "processed_data"

    def process_user(self, conv_idx, locomo_df, frame, version, top_k=20, num_workers=1):
        user_search_path = self.result_dir / f"tmp/{frame}_locomo_search_results_{conv_idx}.json"

        search_results = defaultdict(list)
        response_results = defaultdict(list)

        # Use temporal_locomo data if available, otherwise fall back to original locomo data
        if self.temporal_locomo_data and conv_idx < len(self.temporal_locomo_data):
            temporal_conv = self.temporal_locomo_data[conv_idx]
            conv_id = temporal_conv["conversation_id"]

            # Extract speaker information from temporal data or fallback to default
            if not locomo_df.empty and "conversation" in locomo_df.columns:
                conversation = locomo_df["conversation"].iloc[conv_idx]
                speaker_a = conversation.get("speaker_a", "speaker_a")
                speaker_b = conversation.get("speaker_b", "speaker_b")
            else:
                # Use default speaker names when locomo_df is not available
                speaker_a = "speaker_a"
                speaker_b = "speaker_b"
            speaker_a_user_id = f"{speaker_a}_{conv_idx}"
            speaker_b_user_id = f"{speaker_b}_{conv_idx}"

            # Process temporal data by days
            day_groups = {}
            for day_id, day_data in temporal_conv["days"].items():
                day_groups[day_id] = day_data["qa_pairs"]
        else:
            # Fallback to original locomo data processing
            if locomo_df.empty or "qa" not in locomo_df.columns:
                logger.warning(
                    f"Skipping user {conv_idx}: locomo_df is empty or missing 'qa' column"
                )
                return

            qa_set = locomo_df["qa"].iloc[conv_idx]
            day_groups = self.group_and_sort_qa_by_day(qa_set)

            if not locomo_df.empty and "conversation" in locomo_df.columns:
                conversation = locomo_df["conversation"].iloc[conv_idx]
                speaker_a = conversation.get("speaker_a", "speaker_a")
                speaker_b = conversation.get("speaker_b", "speaker_b")
            else:
                # Use default speaker names when locomo_df is not available
                speaker_a = "speaker_a"
                speaker_b = "speaker_b"
            speaker_a_user_id = f"{speaker_a}_{conv_idx}"
            speaker_b_user_id = f"{speaker_b}_{conv_idx}"
            conv_id = f"locomo_exp_user_{conv_idx}"

        existing_results, loaded = self.load_existing_results(frame, version, conv_idx)
        if loaded:
            print(f"Loaded existing results for group {conv_idx}")
            return existing_results

        metadata = {
            "speaker_a": speaker_a,
            "speaker_b": speaker_b,
            "speaker_a_user_id": speaker_a_user_id,
            "speaker_b_user_id": speaker_b_user_id,
            "conv_idx": conv_idx,
            "conv_id": conv_id,
        }

        reversed_client = None
        if frame in ["memos", "memos_scheduler"]:
            speaker_a_user_id = conv_id + "_speaker_a"
            speaker_b_user_id = conv_id + "_speaker_b"
            client = self.get_client_from_storage(frame, speaker_a_user_id, version, top_k=top_k)
            reversed_client = self.get_client_from_storage(
                frame, speaker_b_user_id, version, top_k=top_k
            )
        else:
            client = self.get_client_from_storage(frame, conv_id, version)

        # ============== func =================
        oai_client = OpenAI(api_key=self.openai_api_key, base_url=self.openai_base_url)

        def process_qa(qa):
            try:
                query = qa.get("question")
                qa_category = qa.get("category")
                if qa_category == 5:
                    return None
                context, search_duration_ms = self.search_query(
                    client, query, metadata, frame, reversed_client=reversed_client, top_k=top_k
                )

                if not context:
                    logger.info(f"No context found for query: {query[:100]}")
                    context = ""

                gold_answer = qa.get("answer")

                answer_start = time()
                answer = self.locomo_response(frame, oai_client, context, query)

                response_duration_ms = (time() - answer_start) * 1000

                # Update detailed query analysis with category information
                conv_key = f"{conv_id}_{speaker_a}_{speaker_b}"
                if hasattr(self, "record_detailed_query_analysis"):
                    # Get the latest memory data from memory_history
                    latest_memory_data = None
                    if self.memory_history.get(conv_key):
                        latest_entry = self.memory_history[conv_key][-1]
                        latest_memory_data = {
                            "speaker_a_memories": latest_entry.get("speaker_a_memories", []),
                            "speaker_b_memories": latest_entry.get("speaker_b_memories", []),
                        }

                    if latest_memory_data:
                        # Update the last query analysis with category
                        with self.stats_lock:
                            query_analysis = self.stats[self.frame][self.version][
                                "detailed_memory_stats"
                            ]["query_analysis"]
                            if query_analysis and query_analysis[-1]["query"] == query:
                                query_analysis[-1]["query_category"] = qa_category
                                query_analysis[-1]["gold_answer"] = gold_answer
                                query_analysis[-1]["generated_answer"] = answer
                                query_analysis[-1]["response_duration_ms"] = response_duration_ms
                                query_analysis[-1]["search_duration_ms"] = search_duration_ms

                logger.info(f"Processed question: {query[:100]}")
                logger.info(f"Answer: {answer[:100]}")
                return {
                    "question": query,
                    "answer": answer,
                    "category": qa_category,
                    "golden_answer": gold_answer,
                    "search_context": context,
                    "response_duration_ms": response_duration_ms,
                    "search_duration_ms": search_duration_ms,
                }
            except Exception as e:
                logger.error(f"Error: {e}. traceback: {traceback.format_exc()}")

        # ===================================
        for day, qa_list in day_groups.items():
            print(f"Processing user {conv_idx} day {day}")
            for qa in tqdm(qa_list, desc=f"Processing user {conv_idx} day {day}"):
                result = process_qa(qa)

                if result:
                    context_preview = (
                        result["search_context"][:20] + "..."
                        if result["search_context"]
                        else "No context"
                    )
                    print(
                        {
                            "question": result["question"][:100],
                            "answer": result["answer"][:100],
                            "category": result["category"],
                            "golden_answer": result["golden_answer"],
                            "search_context": context_preview[:100],
                            "search_duration_ms": result["search_duration_ms"],
                        }
                    )

                    search_results[conv_id].append(
                        {
                            "question": result["question"],
                            "context": result["search_context"],
                            "search_duration_ms": result["search_duration_ms"],
                        }
                    )
                    response_results[conv_id].append(result)
            logger.warning(
                f"Finished processing user {conv_idx} day {day}, data_length: {len(qa_list)}"
            )

        os.makedirs(f"{BASE_DIR}/results/locomo/{frame}-{version}/tmp/", exist_ok=True)
        with open(user_search_path, "w") as fw:
            json.dump(dict(search_results), fw, indent=2)
            print(f"Save search results {conv_idx}")

        # Dump stats after processing each user
        self.dump_stats(iteration=conv_idx)

        return search_results, response_results

    def process_user_wrapper(self, args):
        """
        Wraps the process_user function to support parallel execution and error handling.

        Args:
            args: Tuple containing parameters for process_user

        Returns:
            tuple: Contains user results or error information
        """
        idx, locomo_df, frame, version, top_k, num_workers = args
        try:
            print(f"Processing user {idx}...")
            user_search_results, user_response_results = self.process_user(
                idx, locomo_df, frame, version, top_k, num_workers
            )
            return (user_search_results, user_response_results, None)
        except Exception as e:
            return (None, None, (idx, e, traceback.format_exc()))

    def run_locomo_processing(self):
        """
        Runs the Locomo processing pipeline, including user data processing, memory searching,
        and result persistence.

        Args:
            frame (str): Type of memory framework to use
            version (str): Version identifier for result storage
            num_workers (int): Number of parallel worker threads
            top_k (int): Maximum number of search results to retrieve

        Returns:
            tuple: Contains two dictionaries - all search results and all response results
        """
        load_dotenv()

        frame = self.frame
        version = self.version
        num_workers = self.workers
        top_k = self.top_k

        # Storage for aggregated results
        all_search_results = defaultdict(list)
        all_response_results = defaultdict(list)
        num_users = 10

        # Prepare arguments for each user processing task
        user_args = [
            (idx, self.locomo_df, frame, version, top_k, num_workers) for idx in range(num_users)
        ]

        # === parallel running ====
        if num_workers > 1:
            # Use ThreadPoolExecutor for parallel processing
            with ThreadPoolExecutor(max_workers=num_workers) as executor:
                # Submit all user processing tasks
                future_to_user = {
                    executor.submit(self.process_user_wrapper, args): idx
                    for idx, args in enumerate(user_args)
                }

                # Collect results as they complete
                for future in as_completed(future_to_user):
                    user_idx = future_to_user[future]
                    try:
                        user_search_results, user_response_results, error = future.result()
                        if error is not None:
                            idx, e, traceback_str = error
                            print(f"Error processing user {idx}: {e}. Exception: {traceback_str}")
                        else:
                            # Aggregate results
                            all_search_results[user_idx].extend(user_search_results)
                            all_response_results[user_idx].extend(user_response_results)
                    except Exception as e:
                        print(f"Error processing user {user_idx}: {e}")
        else:
            # Serial processing
            print(
                f"Starting processing for {num_users} users in serial mode, each user using {num_workers} workers for sessions..."
            )
            for idx, args in enumerate(user_args):
                try:
                    user_search_results, user_response_results, error = self.process_user_wrapper(
                        args
                    )
                    if error is not None:
                        user_idx, e, traceback_str = error
                        print(f"Error processing user {user_idx}: {e}. Exception: {traceback_str}")
                    else:
                        # Aggregate results
                        all_search_results[idx].extend(user_search_results)
                        all_response_results[idx].extend(user_response_results)
                except Exception as e:
                    print(f"Error processing user {idx}: {e}")

        # Print evaluation information statistics
        self.print_eval_info()
        self.save_stats()

        # Save all aggregated results
        with open(self.search_path, "w") as fw:
            json.dump(dict(all_search_results), fw, indent=2)
            print(f"Saved all search results to {self.search_path}")

        with open(self.response_path, "w") as fw:
            json.dump(dict(all_response_results), fw, indent=2)
            print(f"Saved all response results to {self.response_path}")

        return dict(all_search_results), dict(all_response_results)
