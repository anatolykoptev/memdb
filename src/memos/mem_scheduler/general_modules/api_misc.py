from typing import Any

from memos.log import get_logger
from memos.mem_scheduler.general_modules.base import BaseSchedulerModule
from memos.mem_scheduler.orm_modules.redis_model import RedisDBManager
from memos.mem_scheduler.schemas.api_schemas import (
    APIMemoryHistoryEntryItem,
    APISearchHistoryManager,
    TaskRunningStatus,
)
from memos.mem_scheduler.utils.db_utils import get_utc_now


logger = get_logger(__name__)


class SchedulerAPIModule(BaseSchedulerModule):
    def __init__(self, window_size=5):
        super().__init__()
        self.window_size = window_size
        self.search_history_managers: dict[str, RedisDBManager] = {}

    def get_search_history_manager(self, user_id: str, mem_cube_id: str) -> RedisDBManager:
        """Get or create a Redis manager for search history."""
        key = f"search_history:{user_id}:{mem_cube_id}"
        if key not in self.search_history_managers:
            self.search_history_managers[key] = RedisDBManager(
                user_id=user_id,
                mem_cube_id=mem_cube_id,
                obj=APISearchHistoryManager(window_size=self.window_size),
            )
        return self.search_history_managers[key]

    def sync_search_data(
        self,
        item_id: str,
        user_id: str,
        mem_cube_id: str,
        query: str,
        formatted_memories: Any,
        running_status: TaskRunningStatus,
        conversation_id: str | None = None,
    ) -> None:
        """
        Sync search data to Redis using APISearchHistoryManager.

        Args:
            item_id: Item identifier (used as task_id)
            user_id: User identifier
            mem_cube_id: Memory cube identifier
            query: Search query string
            formatted_memories: Formatted search results
            running_status: Task running status (RUNNING or COMPLETED)
            conversation_id: Optional conversation identifier
        """
        try:
            # Get the search history manager
            manager = self.get_search_history_manager(user_id, mem_cube_id)

            # Load existing search history
            existing_data = manager.load_from_db()

            if existing_data is None:
                search_history = APISearchHistoryManager(window_size=self.window_size)
            else:
                # Try to load as APISearchHistoryManager, fallback to create new one
                if not isinstance(existing_data, APISearchHistoryManager):
                    logger.error(f"type of existing_data is {type(existing_data)}", exc_info=True)
                search_history = existing_data

            # Check if entry with item_id already exists
            existing_entry, location = search_history.find_entry_by_item_id(item_id)

            if existing_entry is not None:
                # Update existing entry
                success = search_history.update_entry_by_item_id(
                    item_id=item_id,
                    query=query,
                    formatted_memories=formatted_memories,
                    task_status=running_status,  # Use the provided running_status
                    conversation_id=conversation_id,
                )

                if success:
                    logger.info(
                        f"Updated existing entry with item_id: {item_id} in {location} list"
                    )
                else:
                    logger.warning(f"Failed to update entry with item_id: {item_id}")
            else:
                # Create new entry
                search_entry = APIMemoryHistoryEntryItem(
                    task_id=item_id,  # Use item_id as task_id
                    query=query,
                    formatted_memories=formatted_memories,
                    task_status=running_status,  # Use the provided running_status
                    conversation_id=conversation_id,
                    timestamp=get_utc_now(),
                )

                # Add entry based on running_status
                entry_dict = search_entry.to_dict()

                if running_status == TaskRunningStatus.COMPLETED:
                    # Add directly to completed list
                    search_history.completed_entries.append(search_entry)
                    # Maintain window size
                    if len(search_history.completed_entries) > search_history.window_size:
                        search_history.completed_entries = search_history.completed_entries[
                            -search_history.window_size :
                        ]
                else:
                    # Add to running list for RUNNING status
                    search_history.add_running_entry(entry_dict)

                logger.info(
                    f"Created new entry with item_id: {item_id} and status: {running_status}"
                )

            # Save back to Redis
            manager.save_to_db(search_history)

            logger.info(
                f"Synced search data for user {user_id}, mem_cube {mem_cube_id}. "
                f"Running: {len(search_history.running_entries)}, Completed: {len(search_history.completed_entries)}"
            )

        except Exception as e:
            logger.error(f"Failed to sync search data: {e}", exc_info=True)

    def get_pre_memories(self, user_id: str, mem_cube_id: str) -> list:
        manager = self.get_search_history_manager(user_id, mem_cube_id)
        existing_data = manager.load_from_db()

        if existing_data is None:
            return []

        # Handle different data formats for backward compatibility
        if isinstance(existing_data, APISearchHistoryManager):
            search_history = existing_data
        elif isinstance(existing_data, list):
            # Old format: list of entries, return the latest entry's formatted_memories
            if not existing_data:
                return []
            latest_entry = existing_data[-1]  # Get the latest entry
            return latest_entry.get("formatted_memories", [])
        else:
            # Try to convert to APISearchHistoryManager
            try:
                search_history = APISearchHistoryManager(**existing_data)
            except Exception:
                return []

        histor_memories = search_history.get_history_memories(turns=1)
        return histor_memories

    def get_history_memories(self, user_id: str, mem_cube_id: str, n: int) -> list:
        """Get history memories for backward compatibility with tests."""
        manager = self.get_search_history_manager(user_id, mem_cube_id)
        existing_data = manager.load_from_db()

        if existing_data is None:
            return []

        # Handle different data formats
        if isinstance(existing_data, APISearchHistoryManager):
            search_history = existing_data
        else:
            # Try to convert to APISearchHistoryManager
            try:
                search_history = APISearchHistoryManager(**existing_data)
            except Exception:
                return []

        return search_history.get_history_memories(turns=n)
