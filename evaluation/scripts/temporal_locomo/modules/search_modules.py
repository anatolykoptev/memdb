"""
Search modules for different memory frameworks.
"""

import json
import time

from datetime import datetime
from typing import TYPE_CHECKING

from memos.log import get_logger

from .client_manager import EvalModuleWithClientManager


if TYPE_CHECKING:
    from memos.mem_os.main import MOS
from .prompts import (
    SEARCH_PROMPT_MEM0,
    SEARCH_PROMPT_MEM0_GRAPH,
    SEARCH_PROMPT_MEMOS,
    SEARCH_PROMPT_ZEP,
)
from .utils import filter_memory_data


logger = get_logger(__name__)


class EvalModuleWithSearchFunctions(EvalModuleWithClientManager):
    """
    Contains search methods for different memory frameworks.
    """

    def __init__(self, args):
        super().__init__(args=args)

    def mem0_search(self, client, query, speaker_a_user_id, speaker_b_user_id, top_k=20):
        """
        Search memories using the mem0 framework.

        Args:
            client: mem0 client instance
            query: Search query string
            speaker_a_user_id: User ID for first speaker
            speaker_b_user_id: User ID for second speaker
            top_k: Number of results to retrieve

        Returns:
            Tuple containing formatted context and search duration in milliseconds
        """
        start = time.time()
        search_speaker_a_results = client.search(
            query=query,
            top_k=top_k,
            user_id=speaker_a_user_id,
            output_format="v1.1",
            version="v2",
            filters={"AND": [{"user_id": f"{speaker_a_user_id}"}, {"run_id": "*"}]},
        )
        search_speaker_b_results = client.search(
            query=query,
            top_k=top_k,
            user_id=speaker_b_user_id,
            output_format="v1.1",
            version="v2",
            filters={"AND": [{"user_id": f"{speaker_b_user_id}"}, {"run_id": "*"}]},
        )

        # Format speaker A memories
        search_speaker_a_memory = [
            {
                "memory": memory["memory"],
                "timestamp": memory["created_at"],
                "score": round(memory["score"], 2),
            }
            for memory in search_speaker_a_results["results"]
        ]

        search_speaker_a_memory = [
            [f"{item['timestamp']}: {item['memory']}" for item in search_speaker_a_memory]
        ]

        # Format speaker B memories
        search_speaker_b_memory = [
            {
                "memory": memory["memory"],
                "timestamp": memory["created_at"],
                "score": round(memory["score"], 2),
            }
            for memory in search_speaker_b_results["results"]
        ]

        search_speaker_b_memory = [
            [f"{item['timestamp']}: {item['memory']}" for item in search_speaker_b_memory]
        ]

        # Store in memory history
        conv_key = f"{speaker_a_user_id}_{speaker_b_user_id}"
        self.memory_history[conv_key].append(
            {
                "query": query,
                "speaker_a_memories": search_speaker_a_memory[0] if search_speaker_a_memory else [],
                "speaker_b_memories": search_speaker_b_memory[0] if search_speaker_b_memory else [],
                "can_answer_a": len(search_speaker_a_results["results"]) > 0,
                "can_answer_b": len(search_speaker_b_results["results"]) > 0,
                "timestamp": datetime.now(),
            }
        )

        # Determine can_answer status
        can_answer_a = len(search_speaker_a_results["results"]) > 0
        can_answer_b = len(search_speaker_b_results["results"]) > 0

        # Print can answer details if applicable
        memory_data = {
            "speaker_a_memories": search_speaker_a_memory[0] if search_speaker_a_memory else [],
            "speaker_b_memories": search_speaker_b_memory[0] if search_speaker_b_memory else [],
        }
        self._print_can_answer_details(
            query=query,
            can_answer_a=can_answer_a,
            can_answer_b=can_answer_b,
            memory_data=memory_data,
            working_memory_state=None,  # mem0 doesn't have working memory concept
            reason_a=f"Retrieved {len(search_speaker_a_results['results'])} memories"
            if can_answer_a
            else "No relevant memories found",
            reason_b=f"Retrieved {len(search_speaker_b_results['results'])} memories"
            if can_answer_b
            else "No relevant memories found",
        )

        # Update statistics
        with self.stats_lock:
            self.stats[self.frame][self.version]["memory_stats"]["total_queries"] += 1
            if can_answer_a or can_answer_b:
                self.stats[self.frame][self.version]["memory_stats"]["can_answer_count"] += 1

            # Update cannot_answer_count if neither speaker can answer
            if not (can_answer_a or can_answer_b):
                self.stats[self.frame][self.version]["memory_stats"]["cannot_answer_count"] += 1

            # Calculate hit rate
            total_queries = self.stats[self.frame][self.version]["memory_stats"]["total_queries"]
            can_answer_count = self.stats[self.frame][self.version]["memory_stats"][
                "can_answer_count"
            ]
            hit_rate = (can_answer_count / total_queries * 100) if total_queries > 0 else 0
            self.stats[self.frame][self.version]["memory_stats"]["answer_hit_rate"] = hit_rate

        # Create context using template
        context = SEARCH_PROMPT_MEM0.format(
            speaker_1_user_id=speaker_a_user_id.split("_")[0],
            speaker_1_memories=json.dumps(search_speaker_a_memory, indent=4),
            speaker_2_user_id=speaker_b_user_id.split("_")[0],
            speaker_2_memories=json.dumps(search_speaker_b_memory, indent=4),
        )

        duration_ms = (time.time() - start) * 1000
        return context, duration_ms

    def memos_search(self, client, query, conv_id, speaker_a, speaker_b, reversed_client=None):
        """
        Search memories using the memos framework.

        Args:
            client: memos client instance
            query: Search query string
            conv_id: Conversation ID
            speaker_a: First speaker identifier
            speaker_b: Second speaker identifier
            reversed_client: Client instance for reversed speaker context

        Returns:
            Tuple containing formatted context and search duration in milliseconds
        """
        start = time.time()
        # Search memories for speaker A
        search_a_results = client.search(
            query=query,
            user_id=conv_id + "_speaker_a",
        )
        filtered_search_a_results = filter_memory_data(search_a_results)["text_mem"][0]["memories"]
        speaker_a_context = ""
        for item in filtered_search_a_results:
            speaker_a_context += f"{item['memory']}\n"

        # Search memories for speaker B
        search_b_results = reversed_client.search(
            query=query,
            user_id=conv_id + "_speaker_b",
        )
        filtered_search_b_results = filter_memory_data(search_b_results)["text_mem"][0]["memories"]
        speaker_b_context = ""
        for item in filtered_search_b_results:
            speaker_b_context += f"{item['memory']}\n"

        # Store in memory history
        conv_key = f"{conv_id}_{speaker_a}_{speaker_b}"
        self.memory_history[conv_key].append(
            {
                "query": query,
                "speaker_a_memories": [item["memory"] for item in filtered_search_a_results],
                "speaker_b_memories": [item["memory"] for item in filtered_search_b_results],
                "can_answer_a": len(filtered_search_a_results) > 0,
                "can_answer_b": len(filtered_search_b_results) > 0,
                "timestamp": datetime.now(),
            }
        )

        # Determine can_answer status
        can_answer_a = len(filtered_search_a_results) > 0
        can_answer_b = len(filtered_search_b_results) > 0

        # Print can answer details if applicable
        memory_data = {
            "speaker_a_memories": [item["memory"] for item in filtered_search_a_results],
            "speaker_b_memories": [item["memory"] for item in filtered_search_b_results],
        }
        self._print_can_answer_details(
            query=query,
            can_answer_a=can_answer_a,
            can_answer_b=can_answer_b,
            memory_data=memory_data,
            working_memory_state=None,  # memos doesn't have working memory concept
            reason_a=f"Retrieved {len(filtered_search_a_results)} memories"
            if can_answer_a
            else "No relevant memories found",
            reason_b=f"Retrieved {len(filtered_search_b_results)} memories"
            if can_answer_b
            else "No relevant memories found",
        )

        # Update statistics
        with self.stats_lock:
            self.stats[self.frame][self.version]["memory_stats"]["total_queries"] += 1
            if can_answer_a or can_answer_b:
                self.stats[self.frame][self.version]["memory_stats"]["can_answer_count"] += 1

            # Update cannot_answer_count if neither speaker can answer
            if not (can_answer_a or can_answer_b):
                self.stats[self.frame][self.version]["memory_stats"]["cannot_answer_count"] += 1

            # Calculate hit rate
            total_queries = self.stats[self.frame][self.version]["memory_stats"]["total_queries"]
            can_answer_count = self.stats[self.frame][self.version]["memory_stats"][
                "can_answer_count"
            ]
            hit_rate = (can_answer_count / total_queries * 100) if total_queries > 0 else 0
            self.stats[self.frame][self.version]["memory_stats"]["answer_hit_rate"] = hit_rate

        # Create context using template
        context = SEARCH_PROMPT_MEMOS.format(
            speaker_1=speaker_a,
            speaker_1_memories=speaker_a_context,
            speaker_2=speaker_b,
            speaker_2_memories=speaker_b_context,
        )

        duration_ms = (time.time() - start) * 1000
        return context, duration_ms

    def memos_scheduler_search(
        self, client, query, conv_id, speaker_a, speaker_b, reversed_client=None
    ):
        start = time.time()
        client: MOS = client

        # Store memory history for this conversation
        conv_key = f"{conv_id}_{speaker_a}_{speaker_b}"

        if self.scheduler_flag:
            # Search for speaker A
            search_a_results, can_answer_a = client.mem_scheduler.search_for_eval(
                query=query,
                user_id=conv_id + "_speaker_a",
                top_k=client.config.top_k,
                scheduler_flag=True,
            )

            # Search for speaker B
            search_b_results, can_answer_b = reversed_client.mem_scheduler.search_for_eval(
                query=query,
                user_id=conv_id + "_speaker_b",
                top_k=client.config.top_k,
                scheduler_flag=True,
            )

            # Store in memory history
            memory_data = {
                "speaker_a_memories": search_a_results,
                "speaker_b_memories": search_b_results,
            }
            self.memory_history[conv_key].append(
                {
                    "query": query,
                    "speaker_a_memories": search_a_results,
                    "speaker_b_memories": search_b_results,
                    "can_answer_a": can_answer_a,
                    "can_answer_b": can_answer_b,
                    "timestamp": datetime.now(),
                }
            )

            # Update statistics
            with self.stats_lock:
                self.stats[self.frame][self.version]["memory_stats"]["total_queries"] += 1
                if can_answer_a or can_answer_b:
                    self.stats[self.frame][self.version]["memory_stats"]["can_answer_count"] += 1
                else:
                    self.stats[self.frame][self.version]["memory_stats"]["cannot_answer_count"] += 1

                # Calculate hit rate
                total_queries = self.stats[self.frame][self.version]["memory_stats"][
                    "total_queries"
                ]
                can_answer_count = self.stats[self.frame][self.version]["memory_stats"][
                    "can_answer_count"
                ]
                hit_rate = (can_answer_count / total_queries * 100) if total_queries > 0 else 0
                self.stats[self.frame][self.version]["memory_stats"]["answer_hit_rate"] = hit_rate

            # Print can answer details if applicable
            working_memory_state = self._get_working_memory_state(client, conv_id + "_speaker_a")
            self._print_can_answer_details(
                query=query,
                can_answer_a=can_answer_a,
                can_answer_b=can_answer_b,
                memory_data=memory_data,
                working_memory_state=working_memory_state,
                reason_a=f"Retrieved {len(search_a_results)} memories"
                if can_answer_a
                else "No relevant memories found",
                reason_b=f"Retrieved {len(search_b_results)} memories"
                if can_answer_b
                else "No relevant memories found",
            )

            # Record detailed query analysis
            self.record_detailed_query_analysis(
                query=query,
                conv_id=conv_id,
                speaker_a=speaker_a,
                speaker_b=speaker_b,
                can_answer_a=can_answer_a,
                can_answer_b=can_answer_b,
                memory_data=memory_data,
                working_memory_state=working_memory_state,
                query_category=None,  # Will be set in process_qa
            )

            speaker_a_context = ""
            for item in search_a_results:
                speaker_a_context += f"{item}\n"

            speaker_b_context = ""
            for item in search_b_results:
                speaker_b_context += f"{item}\n"

            context = SEARCH_PROMPT_MEMOS.format(
                speaker_1=speaker_a,
                speaker_1_memories=speaker_a_context,
                speaker_2=speaker_b,
                speaker_2_memories=speaker_b_context,
            )
        else:
            start = time.time()
            # 先看working memories再查
            _, can_answer_a = client.mem_scheduler.search_for_eval(
                query=query,
                user_id=conv_id + "_speaker_a",
                top_k=client.config.top_k,
                scheduler_flag=False,
            )

            search_a_results = client.search(
                query=query,
                user_id=conv_id + "_speaker_a",
            )
            filtered_search_a_results = filter_memory_data(search_a_results)["text_mem"][0][
                "memories"
            ]
            speaker_a_context = ""
            for item in filtered_search_a_results:
                speaker_a_context += f"{item['memory']}\n"

            _, can_answer_b = client.mem_scheduler.search_for_eval(
                query=query,
                user_id=conv_id + "_speaker_b",
                top_k=client.config.top_k,
                scheduler_flag=False,
            )

            search_b_results = reversed_client.search(
                query=query,
                user_id=conv_id + "_speaker_b",
            )
            filtered_search_b_results = filter_memory_data(search_b_results)["text_mem"][0][
                "memories"
            ]
            speaker_b_context = ""
            for item in filtered_search_b_results:
                speaker_b_context += f"{item['memory']}\n"

            # Store in memory history
            memory_data = {
                "speaker_a_memories": [item["memory"] for item in filtered_search_a_results],
                "speaker_b_memories": [item["memory"] for item in filtered_search_b_results],
            }
            self.memory_history[conv_key].append(
                {
                    "query": query,
                    "speaker_a_memories": [item["memory"] for item in filtered_search_a_results],
                    "speaker_b_memories": [item["memory"] for item in filtered_search_b_results],
                    "can_answer_a": can_answer_a,
                    "can_answer_b": can_answer_b,
                    "timestamp": datetime.now(),
                }
            )

            # Update statistics
            with self.stats_lock:
                self.stats[self.frame][self.version]["memory_stats"]["total_queries"] += 1
                if can_answer_a or can_answer_b:
                    self.stats[self.frame][self.version]["memory_stats"]["can_answer_count"] += 1
                else:
                    self.stats[self.frame][self.version]["memory_stats"]["cannot_answer_count"] += 1

                # Calculate hit rate
                total_queries = self.stats[self.frame][self.version]["memory_stats"][
                    "total_queries"
                ]
                can_answer_count = self.stats[self.frame][self.version]["memory_stats"][
                    "can_answer_count"
                ]
                hit_rate = (can_answer_count / total_queries * 100) if total_queries > 0 else 0
                self.stats[self.frame][self.version]["memory_stats"]["answer_hit_rate"] = hit_rate

            # Print can answer details if applicable
            working_memory_state = self._get_working_memory_state(client, conv_id + "_speaker_a")
            self._print_can_answer_details(
                query=query,
                can_answer_a=can_answer_a,
                can_answer_b=can_answer_b,
                memory_data=memory_data,
                working_memory_state=working_memory_state,
                reason_a=f"Retrieved {len(filtered_search_a_results)} memories"
                if can_answer_a
                else "No relevant memories found",
                reason_b=f"Retrieved {len(filtered_search_b_results)} memories"
                if can_answer_b
                else "No relevant memories found",
            )

            # Record detailed query analysis
            self.record_detailed_query_analysis(
                query=query,
                conv_id=conv_id,
                speaker_a=speaker_a,
                speaker_b=speaker_b,
                can_answer_a=can_answer_a,
                can_answer_b=can_answer_b,
                memory_data=memory_data,
                working_memory_state=working_memory_state,
                query_category=None,  # Will be set in process_qa
            )

            context = SEARCH_PROMPT_MEMOS.format(
                speaker_1=speaker_a,
                speaker_1_memories=speaker_a_context,
                speaker_2=speaker_b,
                speaker_2_memories=speaker_b_context,
            )

        logger.info(f'query "{query[:100]}", context: {context[:100]}"')
        duration_ms = (time.time() - start) * 1000
        self.print_eval_info()
        return context, duration_ms

    def mem0_graph_search(self, client, query, speaker_a_user_id, speaker_b_user_id, top_k=20):
        start = time.time()
        search_speaker_a_results = client.search(
            query=query,
            top_k=top_k,
            user_id=speaker_a_user_id,
            output_format="v1.1",
            version="v2",
            enable_graph=True,
            filters={"AND": [{"user_id": f"{speaker_a_user_id}"}, {"run_id": "*"}]},
        )
        search_speaker_b_results = client.search(
            query=query,
            top_k=top_k,
            user_id=speaker_b_user_id,
            output_format="v1.1",
            version="v2",
            enable_graph=True,
            filters={"AND": [{"user_id": f"{speaker_b_user_id}"}, {"run_id": "*"}]},
        )

        search_speaker_a_memory = [
            {
                "memory": memory["memory"],
                "timestamp": memory["created_at"],
                "score": round(memory["score"], 2),
            }
            for memory in search_speaker_a_results["results"]
        ]

        search_speaker_a_memory = [
            [f"{item['timestamp']}: {item['memory']}" for item in search_speaker_a_memory]
        ]

        search_speaker_b_memory = [
            {
                "memory": memory["memory"],
                "timestamp": memory["created_at"],
                "score": round(memory["score"], 2),
            }
            for memory in search_speaker_b_results["results"]
        ]

        search_speaker_b_memory = [
            [f"{item['timestamp']}: {item['memory']}" for item in search_speaker_b_memory]
        ]

        search_speaker_a_graph = [
            {
                "source": relation["source"],
                "relationship": relation["relationship"],
                "target": relation["target"],
            }
            for relation in search_speaker_a_results["relations"]
        ]

        search_speaker_b_graph = [
            {
                "source": relation["source"],
                "relationship": relation["relationship"],
                "target": relation["target"],
            }
            for relation in search_speaker_b_results["relations"]
        ]
        context = SEARCH_PROMPT_MEM0_GRAPH.format(
            speaker_1_user_id=speaker_a_user_id.split("_")[0],
            speaker_1_memories=json.dumps(search_speaker_a_memory, indent=4),
            speaker_1_graph_memories=json.dumps(search_speaker_a_graph, indent=4),
            speaker_2_user_id=speaker_b_user_id.split("_")[0],
            speaker_2_memories=json.dumps(search_speaker_b_memory, indent=4),
            speaker_2_graph_memories=json.dumps(search_speaker_b_graph, indent=4),
        )
        print(query, context)
        duration_ms = (time.time() - start) * 1000
        return context, duration_ms

    def zep_search(self, client, query, group_id, top_k=20):
        start = time.time()
        nodes_result = client.graph.search(
            query=query,
            group_id=group_id,
            scope="nodes",
            reranker="rrf",
            limit=top_k,
        )
        edges_result = client.graph.search(
            query=query,
            group_id=group_id,
            scope="edges",
            reranker="cross_encoder",
            limit=top_k,
        )

        nodes = nodes_result.nodes
        edges = edges_result.edges

        facts = [f"  - {edge.fact} (event_time: {edge.valid_at})" for edge in edges]
        entities = [f"  - {node.name}: {node.summary}" for node in nodes]

        # Store in memory history
        conv_key = f"zep_{group_id}"
        self.memory_history[conv_key].append(
            {
                "query": query,
                "facts": facts,
                "entities": entities,
                "can_answer": len(facts) > 0 or len(entities) > 0,
                "timestamp": datetime.now(),
            }
        )

        # Update statistics
        with self.stats_lock:
            self.stats[self.frame][self.version]["memory_stats"]["total_queries"] += 1
            if len(facts) > 0 or len(entities) > 0:
                self.stats[self.frame][self.version]["memory_stats"]["can_answer_count"] += 1

            # Calculate hit rate
            total_queries = self.stats[self.frame][self.version]["memory_stats"]["total_queries"]
            can_answer_count = self.stats[self.frame][self.version]["memory_stats"][
                "can_answer_count"
            ]
            hit_rate = (can_answer_count / total_queries * 100) if total_queries > 0 else 0
            self.stats[self.frame][self.version]["memory_stats"]["answer_hit_rate"] = hit_rate

        context = SEARCH_PROMPT_ZEP.format(facts="\n".join(facts), entities="\n".join(entities))

        duration_ms = (time.time() - start) * 1000

        return context, duration_ms

    def search_query(self, client, query, metadata, frame, reversed_client=None, top_k=20):
        conv_id = metadata.get("conv_id")
        speaker_a = metadata.get("speaker_a")
        speaker_b = metadata.get("speaker_b")
        speaker_a_user_id = metadata.get("speaker_a_user_id")
        speaker_b_user_id = metadata.get("speaker_b_user_id")

        if frame == "zep":
            context, duration_ms = self.zep_search(client, query, conv_id, top_k)
        elif frame == "mem0":
            context, duration_ms = self.mem0_search(
                client, query, speaker_a_user_id, speaker_b_user_id, top_k
            )
        elif frame == "mem0_graph":
            context, duration_ms = self.mem0_graph_search(
                client, query, speaker_a_user_id, speaker_b_user_id, top_k
            )
        elif frame == "memos":
            context, duration_ms = self.memos_search(
                client, query, conv_id, speaker_a, speaker_b, reversed_client
            )
        elif frame == "memos_scheduler":
            context, duration_ms = self.memos_scheduler_search(
                client, query, conv_id, speaker_a, speaker_b, reversed_client
            )
        else:
            raise NotImplementedError()

        return context, duration_ms
