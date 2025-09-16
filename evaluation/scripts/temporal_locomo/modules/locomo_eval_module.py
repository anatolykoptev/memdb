import json
import traceback

from collections import defaultdict
from datetime import datetime

from memos.log import get_logger

from .search_modules import EvalModuleWithSearchFunctions


logger = get_logger(__name__)


class LocomoEvalModelModules(EvalModuleWithSearchFunctions):
    """
    Main evaluation module that combines all functionality for locomo evaluation.
    """

    def __init__(self, args):
        super().__init__(args=args)

    def print_eval_info(self):
        """
        Calculate and print the evaluation information including answer statistics for memory scheduler (thread-safe).
        Shows total queries, can answer count, cannot answer count, and answer hit rate.
        """
        with self.stats_lock:
            # Get statistics
            total_queries = self.stats[self.frame][self.version]["memory_stats"]["total_queries"]
            can_answer_count = self.stats[self.frame][self.version]["memory_stats"][
                "can_answer_count"
            ]
            cannot_answer_count = self.stats[self.frame][self.version]["memory_stats"][
                "cannot_answer_count"
            ]
            hit_rate = self.stats[self.frame][self.version]["memory_stats"]["answer_hit_rate"]

            # Print basic statistics
            print(f"Total Queries: {total_queries}")
            logger.info(f"Total Queries: {total_queries}")

            print(f"Can Answer Count: {can_answer_count}")
            logger.info(f"Can Answer Count: {can_answer_count}")

            print(f"Cannot Answer Count: {cannot_answer_count}")
            logger.info(f"Cannot Answer Count: {cannot_answer_count}")

            # Verify count consistency
            if total_queries != (can_answer_count + cannot_answer_count):
                print(
                    f"WARNING: Count mismatch! Total ({total_queries}) != Can Answer ({can_answer_count}) + Cannot Answer ({cannot_answer_count})"
                )
                logger.warning(
                    f"Count mismatch! Total ({total_queries}) != Can Answer ({can_answer_count}) + Cannot Answer ({cannot_answer_count})"
                )

            print(f"Answer Hit Rate: {hit_rate:.2f}% ({can_answer_count}/{total_queries})")
            logger.info(f"Answer Hit Rate: {hit_rate:.2f}% ({can_answer_count}/{total_queries})")

            print(f"Memory History Entries: {len(self.memory_history)} conversations")
            logger.info(f"Memory History Entries: {len(self.memory_history)} conversations")

            # Display detailed memory stats summary
            detailed_stats = self.stats[self.frame][self.version]["detailed_memory_stats"]
            query_analysis_count = len(detailed_stats["query_analysis"])
            conversation_count = len(detailed_stats["conversation_stats"])

            print(
                f"Detailed Analysis: {query_analysis_count} queries analyzed across {conversation_count} conversations"
            )
            logger.info(
                f"Detailed Analysis: {query_analysis_count} queries analyzed across {conversation_count} conversations"
            )

            # Show conversation-level statistics
            if conversation_count > 0:
                conv_stats = detailed_stats["conversation_stats"]
                avg_queries_per_conv = (
                    sum(stats["total_queries"] for stats in conv_stats.values())
                    / conversation_count
                )
                avg_hit_rate_per_conv = (
                    sum(
                        (stats["can_answer_count"] / stats["total_queries"] * 100)
                        if stats["total_queries"] > 0
                        else 0
                        for stats in conv_stats.values()
                    )
                    / conversation_count
                )

                print(f"Average queries per conversation: {avg_queries_per_conv:.1f}")
                logger.info(f"Average queries per conversation: {avg_queries_per_conv:.1f}")
                print(f"Average hit rate per conversation: {avg_hit_rate_per_conv:.1f}%")
                logger.info(f"Average hit rate per conversation: {avg_hit_rate_per_conv:.1f}%")

    def save_detailed_stats(self):
        """
        Save detailed memory statistics to a separate file.
        """
        try:
            detailed_stats_path = self.stats_dir / "detailed_memory_stats.json"
            with self.stats_lock:
                detailed_stats = self.stats[self.frame][self.version]["detailed_memory_stats"]
                # Convert defaultdict to regular dict for JSON serialization
                serializable_stats = {
                    "query_analysis": detailed_stats["query_analysis"],
                    "conversation_stats": dict(detailed_stats["conversation_stats"]),
                    "working_memory_snapshots": dict(detailed_stats["working_memory_snapshots"]),
                    "query_history": dict(detailed_stats["query_history"]),
                    "memory_retrieval_patterns": dict(detailed_stats["memory_retrieval_patterns"]),
                }

            with open(detailed_stats_path, "w", encoding="utf-8") as f:
                json.dump(serializable_stats, f, indent=2, ensure_ascii=False)

            logger.info(f"Detailed memory stats saved to: {detailed_stats_path}")
        except Exception as e:
            logger.error(f"Failed to save detailed memory stats: {e}")

    def save_stats(self):
        """
        Serializes and saves the contents of self.stats to the specified path:
        Base_dir/results/frame-version/stats

        This method handles directory creation, thread-safe access to statistics data,
        and proper JSON serialization of complex data structures.
        """
        try:
            # Save detailed stats first
            self.save_detailed_stats()

            # Construct the full path for saving statistics using Path

            # Thread-safe access to the stats data using the lock
            with self.stats_lock:
                # Create a copy of the data to prevent modification during serialization
                stats_data = dict(self.stats)

                # Helper function to convert defaultdict to regular dict for JSON serialization
                def convert_defaultdict(obj):
                    if isinstance(obj, defaultdict):
                        return dict(obj)
                    return obj

                # Debug: Print stats summary before saving
                print(f"DEBUG: Saving stats for {self.frame}-{self.version}")
                print(f"DEBUG: Stats path: {self.stats_path}")
                print(f"DEBUG: Stats data keys: {list(stats_data.keys())}")
                if self.frame in stats_data and self.version in stats_data[self.frame]:
                    frame_data = stats_data[self.frame][self.version]
                    print(f"DEBUG: Memory stats: {frame_data.get('memory_stats', {})}")
                    print(
                        f"DEBUG: Total queries: {frame_data.get('memory_stats', {}).get('total_queries', 0)}"
                    )

                # Serialize and save the statistics data to file
                with self.stats_path.open("w", encoding="utf-8") as fw:
                    json.dump(
                        stats_data, fw, ensure_ascii=False, indent=2, default=convert_defaultdict
                    )

            self.logger.info(f"Successfully saved stats to: {self.stats_path}")
            print(f"DEBUG: Stats file created at {self.stats_path}")

        except Exception as e:
            self.logger.error(f"Failed to save stats: {e!s}")
            self.logger.error(traceback.format_exc())
            print(f"DEBUG: Error saving stats: {e}")

    def get_memory_history(self, conv_key: str | None = None):
        """
        Get memory history for a specific conversation or all conversations.

        Args:
            conv_key: Specific conversation key. If None, returns all history.

        Returns:
            dict: Memory history data
        """
        if conv_key:
            return self.memory_history.get(conv_key, [])
        return dict(self.memory_history)

    def get_answer_hit_rate(self):
        """
        Get current answer hit rate statistics.

        Returns:
            dict: Hit rate statistics
        """
        with self.stats_lock:
            return {
                "total_queries": self.stats[self.frame][self.version]["memory_stats"][
                    "total_queries"
                ],
                "can_answer_count": self.stats[self.frame][self.version]["memory_stats"][
                    "can_answer_count"
                ],
                "hit_rate_percentage": self.stats[self.frame][self.version]["memory_stats"][
                    "answer_hit_rate"
                ],
            }

    def record_detailed_query_analysis(
        self,
        query,
        conv_id,
        speaker_a,
        speaker_b,
        can_answer_a,
        can_answer_b,
        memory_data,
        working_memory_state=None,
        query_category=None,
    ):
        """
        Record detailed analysis for a single query.

        Args:
            query: The query string
            conv_id: Conversation ID
            speaker_a: First speaker identifier
            speaker_b: Second speaker identifier
            can_answer_a: Whether speaker A's memories can answer the query
            can_answer_b: Whether speaker B's memories can answer the query
            memory_data: Retrieved memory data
            working_memory_state: Current working memory state (if available)
            query_category: Category of the query (if available)
        """
        conv_key = f"{conv_id}_{speaker_a}_{speaker_b}"
        current_time = datetime.now()

        # Create detailed query analysis record
        query_analysis = {
            "timestamp": current_time,
            "query": query,
            "conv_id": conv_id,
            "speaker_a": speaker_a,
            "speaker_b": speaker_b,
            "can_answer_a": can_answer_a,
            "can_answer_b": can_answer_b,
            "can_answer_combined": can_answer_a or can_answer_b,
            "query_category": query_category,
            "memory_data": memory_data,
            "working_memory_state": working_memory_state,
            "query_length": len(query),
            "memory_count_a": len(memory_data.get("speaker_a_memories", [])),
            "memory_count_b": len(memory_data.get("speaker_b_memories", [])),
        }

        with self.stats_lock:
            # Add to query analysis list
            self.stats[self.frame][self.version]["detailed_memory_stats"]["query_analysis"].append(
                query_analysis
            )

            # Update conversation-specific stats
            if (
                conv_key
                not in self.stats[self.frame][self.version]["detailed_memory_stats"][
                    "conversation_stats"
                ]
            ):
                self.stats[self.frame][self.version]["detailed_memory_stats"]["conversation_stats"][
                    conv_key
                ] = {
                    "total_queries": 0,
                    "can_answer_count": 0,
                    "query_categories": defaultdict(int),
                    "avg_query_length": 0,
                    "avg_memory_count": 0,
                    "first_query_time": current_time,
                    "last_query_time": current_time,
                }

            conv_stats = self.stats[self.frame][self.version]["detailed_memory_stats"][
                "conversation_stats"
            ][conv_key]
            conv_stats["total_queries"] += 1
            if can_answer_a or can_answer_b:
                conv_stats["can_answer_count"] += 1
            if query_category:
                conv_stats["query_categories"][query_category] += 1
            conv_stats["last_query_time"] = current_time

            # Update averages
            total_queries = conv_stats["total_queries"]
            conv_stats["avg_query_length"] = (
                conv_stats["avg_query_length"] * (total_queries - 1) + len(query)
            ) / total_queries
            conv_stats["avg_memory_count"] = (
                conv_stats["avg_memory_count"] * (total_queries - 1)
                + len(memory_data.get("speaker_a_memories", []))
                + len(memory_data.get("speaker_b_memories", []))
            ) / total_queries

            # Record query history
            self.stats[self.frame][self.version]["detailed_memory_stats"]["query_history"][
                conv_key
            ].append(
                {
                    "query": query,
                    "timestamp": current_time,
                    "can_answer": can_answer_a or can_answer_b,
                    "category": query_category,
                }
            )

            # Record working memory snapshot if available
            if working_memory_state:
                self.stats[self.frame][self.version]["detailed_memory_stats"][
                    "working_memory_snapshots"
                ][conv_key].append(
                    {
                        "timestamp": current_time,
                        "working_memory": working_memory_state,
                        "query": query,
                    }
                )

            # Record memory retrieval pattern
            retrieval_pattern = {
                "timestamp": current_time,
                "query": query,
                "retrieved_memories_a": len(memory_data.get("speaker_a_memories", [])),
                "retrieved_memories_b": len(memory_data.get("speaker_b_memories", [])),
                "can_answer_a": can_answer_a,
                "can_answer_b": can_answer_b,
                "memory_types": self._analyze_memory_types(memory_data),
            }
            self.stats[self.frame][self.version]["detailed_memory_stats"][
                "memory_retrieval_patterns"
            ][conv_key].append(retrieval_pattern)

    def _analyze_memory_types(self, memory_data):
        """
        Analyze the types of memories retrieved.

        Args:
            memory_data: Memory data dictionary

        Returns:
            dict: Analysis of memory types
        """
        analysis = {
            "has_temporal_memories": False,
            "has_personal_memories": False,
            "has_factual_memories": False,
            "memory_keywords": set(),
        }

        all_memories = []
        all_memories.extend(memory_data.get("speaker_a_memories", []))
        all_memories.extend(memory_data.get("speaker_b_memories", []))

        for memory in all_memories:
            memory_text = memory if isinstance(memory, str) else str(memory)
            memory_lower = memory_text.lower()

            # Check for temporal indicators
            if any(
                word in memory_lower
                for word in [
                    "yesterday",
                    "today",
                    "tomorrow",
                    "last week",
                    "next month",
                    "ago",
                    "later",
                ]
            ):
                analysis["has_temporal_memories"] = True

            # Check for personal indicators
            if any(
                word in memory_lower
                for word in ["i", "my", "me", "myself", "personal", "feel", "think", "believe"]
            ):
                analysis["has_personal_memories"] = True

            # Check for factual indicators
            if any(
                word in memory_lower
                for word in ["fact", "data", "information", "statistics", "number", "date"]
            ):
                analysis["has_factual_memories"] = True

            # Extract keywords (simple approach)
            words = memory_text.split()[:10]  # First 10 words as keywords
            analysis["memory_keywords"].update([w.lower() for w in words if len(w) > 3])

        analysis["memory_keywords"] = list(analysis["memory_keywords"])
        return analysis

    def get_detailed_memory_stats(self, conv_key=None):
        """
        Get detailed memory statistics.

        Args:
            conv_key: Specific conversation key. If None, returns all stats.

        Returns:
            dict: Detailed memory statistics
        """
        with self.stats_lock:
            detailed_stats = self.stats[self.frame][self.version]["detailed_memory_stats"]

            if conv_key:
                return {
                    "conversation_stats": detailed_stats["conversation_stats"].get(conv_key, {}),
                    "query_history": detailed_stats["query_history"].get(conv_key, []),
                    "working_memory_snapshots": detailed_stats["working_memory_snapshots"].get(
                        conv_key, []
                    ),
                    "memory_retrieval_patterns": detailed_stats["memory_retrieval_patterns"].get(
                        conv_key, []
                    ),
                }
            else:
                return dict(detailed_stats)

    def _get_working_memory_state(self, client, user_id):
        """
        Get current working memory state for a user.

        Args:
            client: MOS client instance
            user_id: User ID

        Returns:
            dict: Working memory state or None if not available
        """
        try:
            if hasattr(client, "mem_scheduler") and hasattr(client.mem_scheduler, "working_memory"):
                working_memory = client.mem_scheduler.working_memory
                if hasattr(working_memory, "get_memories"):
                    memories = working_memory.get_memories(user_id)
                    return {
                        "user_id": user_id,
                        "memory_count": len(memories) if memories else 0,
                        "memories": memories[:5] if memories else [],  # First 5 memories
                        "timestamp": datetime.now(),
                    }
        except Exception as e:
            logger.warning(f"Failed to get working memory state for {user_id}: {e}")
        return None

    def _print_can_answer_details(
        self,
        query,
        can_answer_a,
        can_answer_b,
        memory_data,
        working_memory_state=None,
        reason_a=None,
        reason_b=None,
    ):
        """
        Print detailed information when can_answer is True.

        Args:
            query: The query string
            can_answer_a: Whether speaker A can answer
            can_answer_b: Whether speaker B can answer
            memory_data: Retrieved memory data
            working_memory_state: Current working memory state
            reason_a: Reason for speaker A's can_answer result
            reason_b: Reason for speaker B's can_answer result
        """
        if can_answer_a or can_answer_b:
            print("\n" + "=" * 80)
            print("CAN ANSWER DETAILS")
            print("=" * 80)
            print(f"Query: {query}")
            print(f"Can Answer A: {can_answer_a}")
            print(f"Can Answer B: {can_answer_b}")

            if reason_a:
                print(f"Reason A: {reason_a}")
            if reason_b:
                print(f"Reason B: {reason_b}")

            # Print working memory state if available
            if working_memory_state:
                print("\nWorking Memory State:")
                print(f"  User ID: {working_memory_state.get('user_id', 'N/A')}")
                print(f"  Memory Count: {working_memory_state.get('memory_count', 0)}")
                print(f"  Memories: {working_memory_state.get('memories', [])}")

            # Print retrieved memories
            if memory_data:
                speaker_a_memories = memory_data.get("speaker_a_memories", [])
                speaker_b_memories = memory_data.get("speaker_b_memories", [])

                if speaker_a_memories:
                    print(f"\nSpeaker A Memories ({len(speaker_a_memories)}):")
                    for i, memory in enumerate(speaker_a_memories[:3], 1):  # Show first 3
                        print(f"  {i}. {memory}")
                    if len(speaker_a_memories) > 3:
                        print(f"  ... and {len(speaker_a_memories) - 3} more")

                if speaker_b_memories:
                    print(f"\nSpeaker B Memories ({len(speaker_b_memories)}):")
                    for i, memory in enumerate(speaker_b_memories[:3], 1):  # Show first 3
                        print(f"  {i}. {memory}")
                    if len(speaker_b_memories) > 3:
                        print(f"  ... and {len(speaker_b_memories) - 3} more")

            print("=" * 80 + "\n")
