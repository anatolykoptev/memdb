import json
import sys
import unittest

from datetime import datetime
from pathlib import Path
from unittest.mock import MagicMock, patch

from memos.api.product_models import APISearchRequest
from memos.configs.mem_scheduler import GeneralSchedulerConfig
from memos.mem_scheduler.optimized_scheduler import OptimizedScheduler
from memos.mem_scheduler.schemas.api_schemas import TaskRunningStatus
from memos.mem_scheduler.schemas.general_schemas import SearchMode
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

        # Mock current_mem_cube to avoid None value
        self.scheduler.current_mem_cube = "test_mem_cube_string"

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

        # Mock fast search results
        self.fast_memories = [
            {"content": "fast memory 1", "score": 0.9},
            {"content": "fast memory 2", "score": 0.8},
        ]

        # Mock pre-computed fine memories
        self.pre_fine_memories = [
            {"content": "fine memory 1", "score": 0.95},
            {"content": "fast memory 1", "score": 0.9},  # Duplicate to test deduplication
        ]

    @patch("memos.mem_scheduler.optimized_scheduler.get_utc_now")
    def test_mix_search_memories_with_pre_memories(self, mock_get_utc_now):
        """Test mix_search_memories when pre-computed memories are available."""
        # Setup mocks
        mock_get_utc_now.return_value = datetime.now()

        # Mock search_memories (fast search)
        self.scheduler.search_memories = MagicMock(return_value=self.fast_memories)

        # Mock submit_memory_history_async_task
        test_async_task_id = "async_task_123"
        self.scheduler.submit_memory_history_async_task = MagicMock(return_value=test_async_task_id)

        # Mock api_module methods
        self.scheduler.api_module.get_pre_memories = MagicMock(return_value=self.pre_fine_memories)
        self.scheduler.api_module.sync_search_data = MagicMock()

        # Mock submit_messages
        self.scheduler.submit_messages = MagicMock()

        # Call the method
        result = self.scheduler.mix_search_memories(self.search_req, self.user_context)

        # Verify fast search was performed
        self.scheduler.search_memories.assert_called_once_with(
            search_req=self.search_req,
            user_context=self.user_context,
            mem_cube="test_mem_cube_string",  # This should match current_mem_cube
            mode=SearchMode.FAST,
        )

        # Verify async task was submitted
        self.scheduler.submit_memory_history_async_task.assert_called_once_with(
            search_req=self.search_req, user_context=self.user_context
        )

        # Verify pre-memories were requested
        self.scheduler.api_module.get_pre_memories.assert_called_once_with(
            user_id=self.test_user_id, mem_cube_id=self.test_mem_cube_id
        )

        # Verify sync_search_data was called with deduplicated memories
        self.scheduler.api_module.sync_search_data.assert_called_once()
        call_args = self.scheduler.api_module.sync_search_data.call_args

        self.assertEqual(call_args[1]["item_id"], test_async_task_id)
        self.assertEqual(call_args[1]["user_id"], self.test_user_id)
        self.assertEqual(call_args[1]["mem_cube_id"], self.test_mem_cube_id)
        self.assertEqual(call_args[1]["query"], self.test_query)
        self.assertEqual(call_args[1]["running_status"], TaskRunningStatus.COMPLETED)

        # Check that memories were deduplicated (should have 3 unique memories)
        formatted_memories = call_args[1]["formatted_memories"]
        self.assertEqual(len(formatted_memories), 3)

        # Verify the result contains deduplicated memories
        self.assertIsNotNone(result)

    @patch("memos.mem_scheduler.optimized_scheduler.get_utc_now")
    def test_mix_search_memories_no_pre_memories(self, mock_get_utc_now):
        """Test mix_search_memories when no pre-computed memories are available."""
        # Setup mocks
        mock_get_utc_now.return_value = datetime.now()

        # Mock search_memories (fast search)
        self.scheduler.search_memories = MagicMock(return_value=self.fast_memories)

        # Mock submit_memory_history_async_task
        test_async_task_id = "async_task_123"
        self.scheduler.submit_memory_history_async_task = MagicMock(return_value=test_async_task_id)

        # Mock api_module methods - no pre-memories available
        self.scheduler.api_module.get_pre_memories = MagicMock(return_value=None)
        self.scheduler.api_module.sync_search_data = MagicMock()

        # Mock submit_messages
        self.scheduler.submit_messages = MagicMock()

        # Call the method
        result = self.scheduler.mix_search_memories(self.search_req, self.user_context)

        # Verify fast search was performed
        self.scheduler.search_memories.assert_called_once_with(
            search_req=self.search_req,
            user_context=self.user_context,
            mem_cube="test_mem_cube_string",  # This should match current_mem_cube
            mode=SearchMode.FAST,
        )

        # Verify async task was submitted
        self.scheduler.submit_memory_history_async_task.assert_called_once_with(
            search_req=self.search_req, user_context=self.user_context
        )

        # Verify pre-memories were requested
        self.scheduler.api_module.get_pre_memories.assert_called_once_with(
            user_id=self.test_user_id, mem_cube_id=self.test_mem_cube_id
        )

        # Verify sync_search_data was NOT called since no pre-memories
        self.scheduler.api_module.sync_search_data.assert_not_called()

        # Verify the result is just the fast memories
        self.assertEqual(result, self.fast_memories)

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
        self.assertEqual(
            message.mem_cube, "test_mem_cube_string"
        )  # This should match current_mem_cube
        self.assertEqual(message.timestamp, test_timestamp)

        # Verify the content is properly formatted JSON
        content = json.loads(message.content)
        self.assertEqual(content["search_req"]["query"], self.test_query)
        self.assertEqual(content["search_req"]["user_id"], self.test_user_id)
        self.assertEqual(content["user_context"]["mem_cube_id"], self.test_mem_cube_id)

        # Verify the returned async_task_id matches the message item_id
        self.assertEqual(result, message.item_id)


if __name__ == "__main__":
    unittest.main()
