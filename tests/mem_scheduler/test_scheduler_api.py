import sys
import unittest

from pathlib import Path
from unittest.mock import MagicMock, patch

from memos.mem_scheduler.general_modules.api_misc import SchedulerAPIModule
from memos.mem_scheduler.schemas.api_schemas import (
    APISearchHistoryManager,
    TaskRunningStatus,
)


FILE_PATH = Path(__file__).absolute()
BASE_DIR = FILE_PATH.parent.parent.parent
sys.path.insert(0, str(BASE_DIR))  # Enable execution from any working directory


class TestSchedulerAPIModule(unittest.TestCase):
    """Test cases for SchedulerAPIModule functionality."""

    def setUp(self):
        """Set up test fixtures before each test method."""
        self.api_module = SchedulerAPIModule(window_size=3)
        self.test_user_id = "test_user_123"
        self.test_mem_cube_id = "test_cube_456"
        self.test_item_id = "test_item_789"
        self.test_query = "test query"
        self.test_formatted_memories = [{"memory": "test memory 1"}, {"memory": "test memory 2"}]
        self.test_conversation_id = "conv_123"

    def tearDown(self):
        """Clean up after each test method."""
        # Clear any cached managers
        self.api_module.search_history_managers.clear()

    def test_initialization(self):
        """Test SchedulerAPIModule initialization."""
        # Test default window size
        default_module = SchedulerAPIModule()
        self.assertEqual(default_module.window_size, 5)
        self.assertEqual(len(default_module.search_history_managers), 0)

        # Test custom window size
        custom_module = SchedulerAPIModule(window_size=10)
        self.assertEqual(custom_module.window_size, 10)
        self.assertEqual(len(custom_module.search_history_managers), 0)

    @patch("memos.mem_scheduler.general_modules.api_misc.APIRedisDBManager")
    def test_get_search_history_manager_creation(self, mock_redis_manager):
        """Test creation of new search history manager."""
        mock_manager_instance = MagicMock()
        mock_redis_manager.return_value = mock_manager_instance

        # First call should create new manager
        result = self.api_module.get_search_history_manager(
            self.test_user_id, self.test_mem_cube_id
        )

        # Verify APIRedisDBManager was called with correct parameters
        mock_redis_manager.assert_called_once()
        call_args = mock_redis_manager.call_args
        self.assertEqual(call_args[1]["user_id"], self.test_user_id)
        self.assertEqual(call_args[1]["mem_cube_id"], self.test_mem_cube_id)
        self.assertIsInstance(call_args[1]["obj"], APISearchHistoryManager)

        # Verify manager is cached
        key = f"search_history:{self.test_user_id}:{self.test_mem_cube_id}"
        self.assertIn(key, self.api_module.search_history_managers)
        self.assertEqual(result, mock_manager_instance)

    @patch("memos.mem_scheduler.general_modules.api_misc.APIRedisDBManager")
    def test_get_search_history_manager_caching(self, mock_redis_manager):
        """Test that search history manager is properly cached."""
        mock_manager_instance = MagicMock()
        mock_redis_manager.return_value = mock_manager_instance

        # First call
        result1 = self.api_module.get_search_history_manager(
            self.test_user_id, self.test_mem_cube_id
        )

        # Second call should return cached instance
        result2 = self.api_module.get_search_history_manager(
            self.test_user_id, self.test_mem_cube_id
        )

        # APIRedisDBManager should only be called once
        self.assertEqual(mock_redis_manager.call_count, 1)
        self.assertEqual(result1, result2)

    @patch("memos.mem_scheduler.general_modules.api_misc.APIRedisDBManager")
    def test_sync_search_data_create_new_entry(self, mock_redis_manager):
        """Test sync_search_data creates new entry when item_id doesn't exist."""
        # Setup mock manager
        mock_manager_instance = MagicMock()
        mock_redis_manager.return_value = mock_manager_instance

        # Setup mock APISearchHistoryManager
        mock_api_manager = MagicMock(spec=APISearchHistoryManager)
        mock_api_manager.find_entry_by_item_id.return_value = (
            None,
            "not_found",
        )  # No existing entry (returns tuple)
        mock_api_manager.running_task_ids = []  # Initialize as empty list
        mock_manager_instance.obj = mock_api_manager
        mock_manager_instance.sync_with_redis.return_value = mock_api_manager

        # Mock get_search_history_manager to return our mock manager
        with patch.object(
            self.api_module, "get_search_history_manager", return_value=mock_manager_instance
        ):
            # Call sync_search_data
            self.api_module.sync_search_data(
                item_id=self.test_item_id,
                user_id=self.test_user_id,
                mem_cube_id=self.test_mem_cube_id,
                query=self.test_query,
                memories=[],
                formatted_memories=self.test_formatted_memories,
                running_status=TaskRunningStatus.RUNNING,
            )

            # Verify the manager was called to find existing entry
            mock_api_manager.find_entry_by_item_id.assert_called_once_with(self.test_item_id)

            # Verify add_running_entry was called since status is RUNNING
            mock_api_manager.add_running_entry.assert_called_once()

            # Verify sync_with_redis was called
            mock_manager_instance.sync_with_redis.assert_called_once()

    @patch("memos.mem_scheduler.general_modules.api_misc.APIRedisDBManager")
    def test_sync_search_data_update_existing_entry(self, mock_redis_manager):
        """Test sync_search_data updates existing entry when item_id exists."""
        # Setup mock manager
        mock_manager_instance = MagicMock()
        mock_redis_manager.return_value = mock_manager_instance

        # Setup mock APISearchHistoryManager with existing entry
        mock_api_manager = MagicMock(spec=APISearchHistoryManager)
        mock_existing_entry = {"task_id": self.test_item_id, "query": "old_query"}
        mock_api_manager.find_entry_by_item_id.return_value = (
            mock_existing_entry,
            "running",
        )  # Existing entry found
        mock_api_manager.update_entry_by_item_id.return_value = True  # Update successful
        mock_manager_instance.obj = mock_api_manager
        mock_manager_instance.sync_with_redis.return_value = mock_api_manager

        # Mock get_search_history_manager to return our mock manager
        with patch.object(
            self.api_module, "get_search_history_manager", return_value=mock_manager_instance
        ):
            # Call sync_search_data
            self.api_module.sync_search_data(
                item_id=self.test_item_id,
                user_id=self.test_user_id,
                mem_cube_id=self.test_mem_cube_id,
                query=self.test_query,
                memories=[],
                formatted_memories=self.test_formatted_memories,
                running_status=TaskRunningStatus.RUNNING,
            )

            # Verify the manager was called to find existing entry
            mock_api_manager.find_entry_by_item_id.assert_called_once_with(self.test_item_id)

            # Verify update_entry_by_item_id was called
            mock_api_manager.update_entry_by_item_id.assert_called_once()

            # Verify sync_with_redis was called
            mock_manager_instance.sync_with_redis.assert_called_once()

    @patch("memos.mem_scheduler.general_modules.api_misc.APIRedisDBManager")
    def test_sync_search_data_completed_status(self, mock_redis_manager):
        """Test sync_search_data handles COMPLETED status correctly."""
        # Setup mock manager
        mock_manager_instance = MagicMock()
        mock_redis_manager.return_value = mock_manager_instance

        # Setup mock APISearchHistoryManager
        mock_api_manager = MagicMock(spec=APISearchHistoryManager)
        mock_api_manager.find_entry_by_item_id.return_value = (
            None,
            "not_found",
        )  # No existing entry
        mock_api_manager.completed_entries = []  # Initialize as empty list
        mock_api_manager.window_size = 10
        mock_manager_instance.obj = mock_api_manager
        mock_manager_instance.sync_with_redis.return_value = mock_api_manager

        # Mock get_search_history_manager to return our mock manager
        with patch.object(
            self.api_module, "get_search_history_manager", return_value=mock_manager_instance
        ):
            # Call sync_search_data with COMPLETED status
            self.api_module.sync_search_data(
                item_id=self.test_item_id,
                user_id=self.test_user_id,
                mem_cube_id=self.test_mem_cube_id,
                query=self.test_query,
                memories=[],
                formatted_memories=self.test_formatted_memories,
                running_status=TaskRunningStatus.COMPLETED,
            )

            # Verify the manager was called to find existing entry
            mock_api_manager.find_entry_by_item_id.assert_called_once_with(self.test_item_id)

            # Verify entry was added to completed_entries (not running_task_ids)
            self.assertEqual(len(mock_api_manager.completed_entries), 1)

            # Verify sync_with_redis was called
            mock_manager_instance.sync_with_redis.assert_called_once()

    @patch("memos.mem_scheduler.general_modules.api_misc.APIRedisDBManager")
    def test_sync_search_data_error_handling(self, mock_redis_manager):
        """Test sync_search_data handles errors gracefully."""
        # Setup mock manager to raise an exception
        mock_manager_instance = MagicMock()
        mock_redis_manager.return_value = mock_manager_instance
        mock_manager_instance.obj = None  # This will cause an exception path

        # Mock get_search_history_manager to return our mock manager
        with patch.object(
            self.api_module, "get_search_history_manager", return_value=mock_manager_instance
        ):
            # This should not raise an exception
            try:
                self.api_module.sync_search_data(
                    item_id=self.test_item_id,
                    user_id=self.test_user_id,
                    mem_cube_id=self.test_mem_cube_id,
                    query=self.test_query,
                    memories=[],
                    formatted_memories=self.test_formatted_memories,
                    running_status=TaskRunningStatus.RUNNING,
                )
            except Exception as e:
                self.fail(f"sync_search_data raised an exception: {e}")

    @patch("memos.mem_scheduler.general_modules.api_misc.APIRedisDBManager")
    def test_get_pre_fine_memories_empty_history(self, mock_redis_manager):
        """Test get_pre_fine_memories returns empty list when no history."""
        # Setup mock manager
        mock_manager_instance = MagicMock()
        mock_redis_manager.return_value = mock_manager_instance

        # Setup mock APISearchHistoryManager with empty history
        mock_api_manager = MagicMock(spec=APISearchHistoryManager)
        mock_api_manager.get_history_memories = MagicMock(return_value=[])
        mock_manager_instance.obj = mock_api_manager
        mock_manager_instance.sync_with_redis.return_value = mock_api_manager

        # Call get_pre_fine_memories
        result = self.api_module.get_pre_memories(
            user_id=self.test_user_id, mem_cube_id=self.test_mem_cube_id
        )

        # Verify result is empty list
        self.assertEqual(result, [])


if __name__ == "__main__":
    unittest.main()
