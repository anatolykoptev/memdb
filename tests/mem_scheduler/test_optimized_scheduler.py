import json
import sys
import unittest

from datetime import datetime
from pathlib import Path
from unittest.mock import MagicMock, Mock, patch

from memos.api.product_models import APISearchRequest
from memos.configs.mem_scheduler import GeneralSchedulerConfig
from memos.mem_scheduler.general_modules.api_misc import SchedulerAPIModule
from memos.mem_scheduler.optimized_scheduler import OptimizedScheduler
from memos.mem_scheduler.schemas.api_schemas import APISearchHistoryManager, TaskRunningStatus
from memos.mem_scheduler.schemas.general_schemas import SearchMode
from memos.memories.textual.item import TextualMemoryItem, TextualMemoryMetadata
from memos.reranker.http_bge import HTTPBGEReranker
from memos.types import UserContext


FILE_PATH = Path(__file__).absolute()
BASE_DIR = FILE_PATH.parent.parent.parent
sys.path.insert(0, str(BASE_DIR))  # Enable execution from any working directory


class TestOptimizedScheduler(unittest.TestCase):
    """Test cases for OptimizedScheduler functionality."""

    def setUp(self):
        """Set up test fixtures before each test method."""
        # Create a proper config instead of mock
        self.config = GeneralSchedulerConfig(
            startup_mode="thread",
            thread_pool_max_workers=4,
            enable_parallel_dispatch=True,
            consume_interval_seconds=1.0,
            use_redis_queue=False,
            max_internal_message_queue_size=1000,
            top_k=10,
        )

        # Create scheduler instance with mocked dependencies
        with patch("memos.mem_scheduler.optimized_scheduler.SchedulerAPIModule"):
            self.scheduler = OptimizedScheduler(self.config)

        # Test data
        self.test_user_id = "test_user_123"
        self.test_mem_cube_id = "test_cube_456"
        self.test_session_id = "test_session_789"
        self.test_query = "test search query"

        # Create test search request
        self.search_req = APISearchRequest(
            query=self.test_query,
            user_id=self.test_user_id,
            session_id=self.test_session_id,
            top_k=10,
            internet_search=False,
            moscube=False,  # Changed from None to False
            chat_history=[],
        )

        # Create test user context
        self.user_context = UserContext(mem_cube_id=self.test_mem_cube_id)

        # Mock fast search results - should be TextualMemoryItem objects
        self.fast_memories = [
            TextualMemoryItem(
                memory="fast memory 1",
                metadata=TextualMemoryMetadata(
                    user_id=self.test_user_id, session_id=self.test_session_id
                ),
            ),
            TextualMemoryItem(
                memory="fast memory 2",
                metadata=TextualMemoryMetadata(
                    user_id=self.test_user_id, session_id=self.test_session_id
                ),
            ),
        ]

        # Mock pre-computed fine memories - should be dict objects from get_pre_memories
        self.pre_fine_memories = [
            {"memory": "fine memory 1", "score": 0.9},
            {"memory": "fast memory 1", "score": 0.8},  # Duplicate to test deduplication
        ]

        # Mock current_mem_cube as a string to match ScheduleMessageItem validation
        self.scheduler.current_mem_cube = "test_mem_cube_string"

    @patch("memos.mem_scheduler.optimized_scheduler.get_utc_now")
    def test_mix_search_memories_with_pre_memories(self, mock_get_utc_now):
        """Test mix_search_memories when pre-computed memories are available."""
        # Setup mocks
        mock_get_utc_now.return_value = datetime.now()

        # Mock current_mem_cube with proper structure
        mock_mem_cube = MagicMock()
        mock_reranker = MagicMock()
        mock_mem_cube.text_mem.reranker = mock_reranker
        mock_reranker.rerank.return_value = [
            TextualMemoryItem(memory="reranked memory 1", metadata=TextualMemoryMetadata()),
            TextualMemoryItem(memory="reranked memory 2", metadata=TextualMemoryMetadata()),
        ]
        self.scheduler.current_mem_cube = mock_mem_cube

        # Mock search_memories (fast search)
        self.scheduler.search_memories = MagicMock(return_value=self.fast_memories)

        # Mock submit_memory_history_async_task
        test_async_task_id = "async_task_123"
        self.scheduler.submit_memory_history_async_task = MagicMock(return_value=test_async_task_id)

        # Mock api_module methods - get_pre_memories should return TextualMemoryItem objects
        pre_memories = [
            TextualMemoryItem(memory="fine memory 1", metadata=TextualMemoryMetadata()),
            TextualMemoryItem(
                memory="fast memory 1", metadata=TextualMemoryMetadata()
            ),  # Duplicate to test deduplication
        ]
        self.scheduler.api_module.get_pre_memories = MagicMock(return_value=pre_memories)
        self.scheduler.api_module.sync_search_data = MagicMock()

        # Mock submit_messages
        self.scheduler.submit_messages = MagicMock()

        # Call the method
        result = self.scheduler.mix_search_memories(self.search_req, self.user_context)

        # Verify fast search was performed
        self.scheduler.search_memories.assert_called_once_with(
            search_req=self.search_req,
            user_context=self.user_context,
            mem_cube=mock_mem_cube,
            mode=SearchMode.FAST,
        )

        # Verify async task was submitted
        self.scheduler.submit_memory_history_async_task.assert_called_once_with(
            search_req=self.search_req, user_context=self.user_context
        )

        # Verify pre-memories were retrieved
        self.scheduler.api_module.get_pre_memories.assert_called_once_with(
            user_id=self.test_user_id, mem_cube_id=self.test_mem_cube_id
        )

        # Verify reranker was called
        mock_reranker.rerank.assert_called_once()

        # Verify sync_search_data was called
        self.scheduler.api_module.sync_search_data.assert_called_once()

        # Verify result is not None
        self.assertIsNotNone(result)

    @patch("memos.mem_scheduler.optimized_scheduler.get_utc_now")
    def test_mix_search_memories_no_pre_memories(self, mock_get_utc_now):
        """Test mix_search_memories when no pre-memories are available."""
        mock_get_utc_now.return_value = datetime.now()

        # Mock dependencies
        self.scheduler.search_memories = MagicMock(return_value=self.fast_memories)
        self.scheduler.submit_memory_history_async_task = MagicMock(return_value="async_123")

        # Mock API module to return empty pre-memories
        self.scheduler.api_module.get_pre_memories = MagicMock(return_value=[])

        # Mock mem_cube
        mock_mem_cube = MagicMock()
        self.scheduler.current_mem_cube = mock_mem_cube

        # Mock format_textual_memory_item
        with patch(
            "memos.mem_scheduler.optimized_scheduler.format_textual_memory_item"
        ) as mock_format:
            mock_format.side_effect = lambda x: f"formatted_{x.memory}"

            # Call the method
            result = self.scheduler.mix_search_memories(self.search_req, self.user_context)

            # Verify result
            self.assertIsNotNone(result)
            self.assertEqual(len(result), 2)  # Should return formatted fast memories

            # Verify format was called for each fast memory
            self.assertEqual(mock_format.call_count, 2)

            # Verify sync_search_data was NOT called since no pre-memories
            self.scheduler.api_module.sync_search_data.assert_not_called()

        # Verify the result is formatted memories from fast search only
        self.assertIsNotNone(result)
        self.assertIsInstance(result, list)
        # Since no pre-memories, should return formatted fast memories
        self.assertEqual(len(result), len(self.fast_memories))

    @patch("memos.mem_scheduler.optimized_scheduler.get_utc_now")
    def test_submit_memory_history_async_task(self, mock_get_utc_now):
        """Test submit_memory_history_async_task creates correct message."""
        # Setup mocks
        test_timestamp = datetime.now()
        mock_get_utc_now.return_value = test_timestamp

        # Mock submit_messages
        self.scheduler.submit_messages = MagicMock()

        # Call the method
        result = self.scheduler.submit_memory_history_async_task(self.search_req, self.user_context)

        # Verify submit_messages was called
        self.scheduler.submit_messages.assert_called_once()

        # Check the message that was submitted
        submitted_messages = self.scheduler.submit_messages.call_args[0][0]
        self.assertEqual(len(submitted_messages), 1)

        message = submitted_messages[0]
        self.assertTrue(message.item_id.startswith(f"mix_search_{self.test_user_id}_"))
        self.assertEqual(message.user_id, self.test_user_id)
        self.assertEqual(message.mem_cube_id, self.test_mem_cube_id)
        self.assertEqual(message.mem_cube, self.scheduler.current_mem_cube)
        self.assertEqual(message.timestamp, test_timestamp)

        # Verify the content is properly formatted JSON
        content = json.loads(message.content)
        self.assertEqual(content["search_req"]["query"], self.test_query)
        self.assertEqual(content["search_req"]["user_id"], self.test_user_id)
        self.assertEqual(content["user_context"]["mem_cube_id"], self.test_mem_cube_id)

        # Verify the returned async_task_id matches the message item_id
        self.assertEqual(result, message.item_id)

    def test_get_pre_memories_with_valid_data(self):
        """Test get_pre_memories returns correct data when valid history exists."""
        # Create a mock API module
        api_module = SchedulerAPIModule()

        # Mock the manager and its methods
        mock_manager = MagicMock()

        # Create a proper APISearchHistoryManager mock
        mock_search_history = MagicMock(spec=APISearchHistoryManager)
        expected_memories = [
            TextualMemoryItem(memory="pre memory 1", metadata=TextualMemoryMetadata()),
            TextualMemoryItem(memory="pre memory 2", metadata=TextualMemoryMetadata()),
        ]
        mock_search_history.get_history_memories.return_value = expected_memories

        # Make load_from_db return the APISearchHistoryManager mock
        mock_manager.load_from_db.return_value = mock_search_history

        with patch.object(api_module, "get_search_history_manager", return_value=mock_manager):
            result = api_module.get_pre_memories(self.test_user_id, self.test_mem_cube_id)

        # Verify the result
        self.assertEqual(result, expected_memories)
        mock_manager.load_from_db.assert_called_once()
        mock_search_history.get_history_memories.assert_called_once_with(turns=1)

    def test_get_pre_memories_no_data(self):
        """Test get_pre_memories returns empty list when no data exists."""
        api_module = SchedulerAPIModule()

        mock_manager = MagicMock()
        mock_manager.load_from_db.return_value = None

        with patch.object(api_module, "get_search_history_manager", return_value=mock_manager):
            result = api_module.get_pre_memories(self.test_user_id, self.test_mem_cube_id)

        self.assertEqual(result, [])

    def test_get_pre_memories_legacy_format(self):
        """Test get_pre_memories handles legacy list format correctly."""
        api_module = SchedulerAPIModule()

        mock_manager = MagicMock()
        legacy_data = [
            {"formatted_memories": ["legacy memory 1", "legacy memory 2"]},
            {"formatted_memories": ["latest memory 1", "latest memory 2"]},
        ]
        mock_manager.load_from_db.return_value = legacy_data

        with patch.object(api_module, "get_search_history_manager", return_value=mock_manager):
            result = api_module.get_pre_memories(self.test_user_id, self.test_mem_cube_id)

        # Should return the latest entry's formatted_memories
        self.assertEqual(result, ["latest memory 1", "latest memory 2"])

    def test_sync_search_data_new_entry_running(self):
        """Test sync_search_data creates new entry with RUNNING status."""
        api_module = SchedulerAPIModule()

        mock_manager = MagicMock()
        mock_search_history = MagicMock()
        mock_search_history.find_entry_by_item_id.return_value = (None, "not_found")
        mock_search_history.running_task_ids = []
        mock_search_history.completed_entries = []
        mock_manager.load_from_db.return_value = mock_search_history

        test_memories = [TextualMemoryItem(memory="test memory", metadata=TextualMemoryMetadata())]

        with patch.object(api_module, "get_search_history_manager", return_value=mock_manager):
            api_module.sync_search_data(
                item_id="test_item_123",
                user_id=self.test_user_id,
                mem_cube_id=self.test_mem_cube_id,
                query=self.test_query,
                memories=test_memories,
                formatted_memories=["formatted memory"],
                running_status=TaskRunningStatus.RUNNING,
            )

        # Verify manager methods were called
        mock_manager.load_from_db.assert_called_once()
        mock_manager.save_to_db.assert_called_once()
        mock_search_history.find_entry_by_item_id.assert_called_once_with("test_item_123")
        mock_search_history.add_running_entry.assert_called_once()

    def test_sync_search_data_new_entry_completed(self):
        """Test sync_search_data creates new entry with COMPLETED status."""
        api_module = SchedulerAPIModule()

        mock_manager = MagicMock()
        mock_search_history = MagicMock()
        mock_search_history.find_entry_by_item_id.return_value = (None, "not_found")
        mock_search_history.running_task_ids = []
        mock_search_history.completed_entries = []
        mock_search_history.window_size = 5
        mock_manager.load_from_db.return_value = mock_search_history

        test_memories = [TextualMemoryItem(memory="test memory", metadata=TextualMemoryMetadata())]

        with patch.object(api_module, "get_search_history_manager", return_value=mock_manager):
            api_module.sync_search_data(
                item_id="test_item_123",
                user_id=self.test_user_id,
                mem_cube_id=self.test_mem_cube_id,
                query=self.test_query,
                memories=test_memories,
                formatted_memories=["formatted memory"],
                running_status=TaskRunningStatus.COMPLETED,
            )

        # Verify completed entry was added
        self.assertEqual(len(mock_search_history.completed_entries), 1)
        mock_manager.save_to_db.assert_called_once()

    def test_sync_search_data_update_existing(self):
        """Test sync_search_data updates existing entry."""
        api_module = SchedulerAPIModule()

        mock_manager = MagicMock()
        mock_search_history = MagicMock()
        existing_entry = {"task_id": "test_item_123", "query": "old query"}
        mock_search_history.find_entry_by_item_id.return_value = (existing_entry, "running")
        mock_search_history.update_entry_by_item_id.return_value = True
        mock_manager.load_from_db.return_value = mock_search_history

        with patch.object(api_module, "get_search_history_manager", return_value=mock_manager):
            api_module.sync_search_data(
                item_id="test_item_123",
                user_id=self.test_user_id,
                mem_cube_id=self.test_mem_cube_id,
                query="updated query",
                memories=[],
                formatted_memories=["updated memory"],
                running_status=TaskRunningStatus.COMPLETED,
            )

        # Verify update was called
        mock_search_history.update_entry_by_item_id.assert_called_once_with(
            item_id="test_item_123",
            query="updated query",
            formatted_memories=["updated memory"],
            task_status=TaskRunningStatus.COMPLETED,
            conversation_id=None,
            memories=[],
        )

    @patch("requests.post")
    def test_reranker_rerank_success(self, mock_post):
        """Test HTTPBGEReranker.rerank with successful HTTP response."""
        # Setup mock response
        mock_response = Mock()
        mock_response.raise_for_status.return_value = None
        mock_response.json.return_value = {
            "results": [{"index": 1, "relevance_score": 0.9}, {"index": 0, "relevance_score": 0.7}]
        }
        mock_post.return_value = mock_response

        # Create reranker instance
        reranker = HTTPBGEReranker(
            reranker_url="http://test-reranker.com/rerank", model="test-model"
        )

        # Test data
        test_items = [
            TextualMemoryItem(memory="item 1", metadata=TextualMemoryMetadata()),
            TextualMemoryItem(memory="item 2", metadata=TextualMemoryMetadata()),
        ]

        # Call rerank
        result = reranker.rerank(query="test query", graph_results=test_items, top_k=2)

        # Verify results
        self.assertEqual(len(result), 2)
        # Results should be sorted by score (highest first)
        self.assertEqual(result[0][0].memory, "item 2")  # index 1, score 0.9
        self.assertEqual(result[1][0].memory, "item 1")  # index 0, score 0.7
        self.assertAlmostEqual(result[0][1], 0.9)
        self.assertAlmostEqual(result[1][1], 0.7)

        # Verify HTTP request was made
        mock_post.assert_called_once()
        call_args = mock_post.call_args
        self.assertEqual(call_args[0][0], "http://test-reranker.com/rerank")
        self.assertEqual(call_args[1]["json"]["query"], "test query")
        self.assertEqual(call_args[1]["json"]["model"], "test-model")

    @patch("requests.post")
    def test_reranker_rerank_empty_results(self, mock_post):
        """Test HTTPBGEReranker.rerank with empty input."""
        reranker = HTTPBGEReranker(
            reranker_url="http://test-reranker.com/rerank", model="test-model"
        )

        result = reranker.rerank(query="test query", graph_results=[], top_k=5)

        self.assertEqual(result, [])
        mock_post.assert_not_called()

    @patch("requests.post")
    def test_reranker_rerank_http_error(self, mock_post):
        """Test HTTPBGEReranker.rerank handles HTTP errors gracefully."""
        # Setup mock to raise HTTP error
        mock_post.side_effect = Exception("HTTP Error")

        reranker = HTTPBGEReranker(
            reranker_url="http://test-reranker.com/rerank", model="test-model"
        )

        test_items = [TextualMemoryItem(memory="item 1", metadata=TextualMemoryMetadata())]

        # Should not raise exception, return fallback results
        result = reranker.rerank(query="test query", graph_results=test_items, top_k=1)

        # Should return original items with 0.0 scores as fallback
        self.assertEqual(len(result), 1)
        self.assertEqual(result[0][0].memory, "item 1")
        self.assertEqual(result[0][1], 0.0)

    @patch("requests.post")
    def test_reranker_rerank_alternative_response_format(self, mock_post):
        """Test HTTPBGEReranker.rerank with alternative response format."""
        # Setup mock response with "data" format instead of "results"
        mock_response = Mock()
        mock_response.raise_for_status.return_value = None
        mock_response.json.return_value = {"data": [{"score": 0.8}, {"score": 0.6}]}
        mock_post.return_value = mock_response

        reranker = HTTPBGEReranker(
            reranker_url="http://test-reranker.com/rerank", model="test-model"
        )

        test_items = [
            TextualMemoryItem(memory="item 1", metadata=TextualMemoryMetadata()),
            TextualMemoryItem(memory="item 2", metadata=TextualMemoryMetadata()),
        ]

        result = reranker.rerank(query="test query", graph_results=test_items, top_k=2)

        # Verify results are sorted by score
        self.assertEqual(len(result), 2)
        self.assertAlmostEqual(result[0][1], 0.8)
        self.assertAlmostEqual(result[1][1], 0.6)

    def test_mix_search_memories_integration(self):
        """Integration test for mix_search_memories with all components."""
        # Setup comprehensive mocks
        with patch("memos.mem_scheduler.optimized_scheduler.get_utc_now") as mock_get_utc_now:
            mock_get_utc_now.return_value = datetime.now()

            # Mock all dependencies
            self.scheduler.search_memories = MagicMock(return_value=self.fast_memories)
            self.scheduler.submit_memory_history_async_task = MagicMock(return_value="async_123")

            # Mock API module methods - get_pre_memories returns TextualMemoryItem objects
            pre_memories = [
                TextualMemoryItem(memory="pre memory 1", metadata=TextualMemoryMetadata()),
                TextualMemoryItem(memory="pre memory 2", metadata=TextualMemoryMetadata()),
            ]
            self.scheduler.api_module.get_pre_memories = MagicMock(return_value=pre_memories)
            self.scheduler.api_module.sync_search_data = MagicMock()

            # Mock mem_cube and reranker properly
            mock_mem_cube = MagicMock()
            mock_text_mem = MagicMock()
            mock_reranker = MagicMock()

            # Setup reranker to return sorted results as tuples (item, score)
            reranked_results = [
                (self.fast_memories[0], 0.9),
                (pre_memories[0], 0.8),
                (self.fast_memories[1], 0.7),
            ]
            mock_reranker.rerank.return_value = reranked_results
            mock_text_mem.reranker = mock_reranker
            mock_mem_cube.text_mem = mock_text_mem

            # Set current_mem_cube to the mock object
            self.scheduler.current_mem_cube = mock_mem_cube

            # Mock format_textual_memory_item to handle the reranker results
            with patch(
                "memos.mem_scheduler.optimized_scheduler.format_textual_memory_item"
            ) as mock_format:
                mock_format.side_effect = (
                    lambda x: f"formatted_{x[0].memory}"
                    if isinstance(x, tuple)
                    else f"formatted_{x.memory}"
                )

                # Call the method
                result = self.scheduler.mix_search_memories(self.search_req, self.user_context)

            # Verify all components were called correctly

            # 1. Fast search was performed
            self.scheduler.search_memories.assert_called_once_with(
                search_req=self.search_req,
                user_context=self.user_context,
                mem_cube=mock_mem_cube,
                mode=SearchMode.FAST,
            )

            # 2. Pre-memories were retrieved
            self.scheduler.api_module.get_pre_memories.assert_called_once_with(
                user_id=self.test_user_id, mem_cube_id=self.test_mem_cube_id
            )

            # 3. Reranker was called with combined memories
            mock_reranker.rerank.assert_called_once()
            rerank_call_args = mock_reranker.rerank.call_args
            self.assertEqual(rerank_call_args[1]["query"], self.test_query)
            self.assertEqual(rerank_call_args[1]["top_k"], 10)

            # Verify combined memories were passed (should be deduplicated)
            combined_memories = rerank_call_args[1]["graph_results"]
            self.assertEqual(len(combined_memories), 4)  # 2 fast + 2 pre memories

            # 4. Search data was synced
            self.scheduler.api_module.sync_search_data.assert_called_once()
            sync_call_args = self.scheduler.api_module.sync_search_data.call_args
            self.assertEqual(sync_call_args[1]["item_id"], "async_123")
            self.assertEqual(sync_call_args[1]["user_id"], self.test_user_id)
            self.assertEqual(sync_call_args[1]["query"], self.test_query)
            self.assertEqual(sync_call_args[1]["running_status"], TaskRunningStatus.COMPLETED)

            # 5. Verify final result
            self.assertIsNotNone(result)
            self.assertIsInstance(result, list)
            self.assertEqual(len(result), 3)  # Should return 3 formatted results from reranker


if __name__ == "__main__":
    unittest.main()
