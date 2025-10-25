from datetime import datetime
from enum import Enum
from typing import Any
from uuid import uuid4

from pydantic import BaseModel, ConfigDict, Field, field_serializer

from memos.log import get_logger
from memos.mem_scheduler.general_modules.misc import DictConversionMixin
from memos.mem_scheduler.utils.db_utils import get_utc_now


logger = get_logger(__name__)


class TaskRunningStatus(str, Enum):
    """Enumeration for task running status values."""

    RUNNING = "running"
    COMPLETED = "completed"


class APIMemoryHistoryEntryItem(BaseModel, DictConversionMixin):
    """Data class for search entry items stored in Redis."""

    task_id: str = Field(
        description="Unique identifier for the task", default_factory=lambda: str(uuid4())
    )
    query: str = Field(..., description="Search query string")
    formatted_memories: Any = Field(..., description="Formatted search results")
    task_status: str = Field(
        default="running", description="Task status: running, completed, failed"
    )
    conversation_id: str | None = Field(
        default=None, description="Optional conversation identifier"
    )
    created_time: datetime = Field(description="Entry creation time", default_factory=get_utc_now)
    timestamp: datetime | None = Field(default=None, description="Timestamp for the entry")

    model_config = ConfigDict(
        arbitrary_types_allowed=True,
        validate_assignment=True,
    )

    @field_serializer("created_time")
    def serialize_created_time(self, value: datetime) -> str:
        """Serialize datetime to ISO format string."""
        return value.isoformat()


class APISearchHistoryManager(BaseModel, DictConversionMixin):
    """
    Data structure for managing search history with separate completed and running entries.
    Supports window_size to limit the number of completed entries.
    """

    window_size: int = Field(default=5, description="Maximum number of completed entries to keep")
    completed_entries: list[APIMemoryHistoryEntryItem] = Field(
        default_factory=list, description="List of completed search entries"
    )
    running_entries: list[APIMemoryHistoryEntryItem] = Field(
        default_factory=list, description="List of running search entries"
    )

    model_config = ConfigDict(
        arbitrary_types_allowed=True,
        validate_assignment=True,
    )

    def add_running_entry(self, entry: dict[str, Any]) -> None:
        """Add a new running entry."""
        self.running_entries.append(entry)
        logger.debug(f"Added running entry with task_id: {entry.get('task_id', 'unknown')}")

    def complete_entry(self, task_id: str) -> bool:
        """
        Move an entry from running to completed list by task_id.

        Args:
            task_id: The task ID to complete

        Returns:
            True if entry was found and moved, False otherwise
        """
        for i, entry in enumerate(self.running_entries):
            if entry.get("task_id") == task_id:
                # Move to completed list
                completed_entry = self.running_entries.pop(i)
                self.completed_entries.append(completed_entry)

                # Maintain window size for completed entries
                if len(self.completed_entries) > self.window_size:
                    # Remove oldest entries (keep only the latest window_size entries)
                    self.completed_entries = self.completed_entries[-self.window_size :]

                logger.debug(f"Completed entry with task_id: {task_id}")
                return True

        logger.warning(f"Task ID {task_id} not found in running entries")
        return False

    def update_entry_status(self, task_id: str, new_status: TaskRunningStatus) -> bool:
        """
        Update the status of an entry (in running list).

        Args:
            task_id: The task ID to update
            new_status: The new status value

        Returns:
            True if entry was found and updated, False otherwise
        """
        for entry in self.running_entries:
            if entry.get("task_id") == task_id:
                entry["task_status"] = new_status
                logger.debug(f"Updated task_id {task_id} status to: {new_status}")
                return True

        logger.warning(f"Task ID {task_id} not found in running entries for status update")
        return False

    def get_running_entries(self) -> list[dict[str, Any]]:
        """Get all running entries"""
        return self.running_entries.copy()

    def get_completed_entries(self) -> list[dict[str, Any]]:
        """Get all completed entries"""
        return self.completed_entries.copy()

    def get_history_memory_entries(self, turns: int | None = None) -> list[dict[str, Any]]:
        """
        Get the most recent n completed search entries, sorted by created_time.

        Args:
            turns: Number of entries to return. If None, returns all completed entries.

        Returns:
            List of completed search entries, sorted by created_time (newest first)
        """
        if not self.completed_entries:
            return []

        # Sort by created_time (newest first)
        sorted_entries = sorted(
            self.completed_entries, key=lambda x: x.get("created_time", ""), reverse=True
        )

        if turns is None:
            return sorted_entries

        return sorted_entries[:turns]

    def get_history_memories(self, turns: int | None = None) -> list[dict[str, Any]]:
        """
        Get the most recent n completed search entries, sorted by created_time.

        Args:
            turns: Number of entries to return. If None, returns all completed entries.

        Returns:
            List of completed search entries, sorted by created_time (newest first)
        """
        sorted_entries = self.get_history_memory_entries(turns=turns)

        formatted_memories = []
        for one in sorted_entries:
            formatted_memories.extend(one.formatted_memories)
        return formatted_memories

    def remove_running_entry(self, task_id: str) -> bool:
        """
        Remove a running entry by task_id (for cleanup/cancellation).

        Args:
            task_id: The task ID to remove

        Returns:
            True if entry was found and removed, False otherwise
        """
        for i, entry in enumerate(self.running_entries):
            if entry.get("task_id") == task_id:
                self.running_entries.pop(i)
                logger.debug(f"Removed running entry with task_id: {task_id}")
                return True

        logger.warning(f"Task ID {task_id} not found in running entries for removal")
        return False

    def find_entry_by_item_id(self, item_id: str) -> tuple[dict[str, Any] | None, str]:
        """
        Find an entry by item_id in both running and completed lists.

        Args:
            item_id: The item ID to search for (could be task_id or other identifier)

        Returns:
            Tuple of (entry_dict, location) where location is 'running', 'completed', or 'not_found'
        """
        # First check running entries
        for entry in self.running_entries:
            if entry.get("task_id") == item_id:
                return entry, "running"

        # Then check completed entries
        for entry in self.completed_entries:
            if entry.get("task_id") == item_id:
                return entry, "completed"

        return None, "not_found"

    def update_entry_by_item_id(
        self,
        item_id: str,
        query: str,
        formatted_memories: Any,
        task_status: TaskRunningStatus,
        conversation_id: str | None = None,
    ) -> bool:
        """
        Update an existing entry by item_id and handle status changes.
        If status changes between RUNNING and COMPLETED, move entry between lists.

        Args:
            item_id: The item ID to update
            query: New query string
            formatted_memories: New formatted memories
            task_status: New task status
            conversation_id: New conversation ID

        Returns:
            True if entry was found and updated, False otherwise
        """
        # Find the entry
        entry, location = self.find_entry_by_item_id(item_id)

        if entry is None:
            return False

        # Update the entry content
        entry["query"] = query
        entry["formatted_memories"] = formatted_memories
        entry["task_status"] = task_status
        if conversation_id is not None:
            entry["conversation_id"] = conversation_id

        # Check if we need to move the entry between lists
        current_is_completed = location == "completed"
        new_is_completed = task_status == TaskRunningStatus.COMPLETED

        if current_is_completed != new_is_completed:
            # Status changed, need to move entry between lists
            if new_is_completed:
                # Move from running to completed
                for i, running_entry in enumerate(self.running_entries):
                    if running_entry.get("task_id") == item_id:
                        moved_entry = self.running_entries.pop(i)
                        self.completed_entries.append(moved_entry)

                        # Maintain window size for completed entries
                        if len(self.completed_entries) > self.window_size:
                            self.completed_entries = self.completed_entries[-self.window_size :]

                        logger.debug(
                            f"Moved entry with item_id: {item_id} from running to completed"
                        )
                        break
            else:
                # Move from completed to running
                for i, completed_entry in enumerate(self.completed_entries):
                    if completed_entry.get("task_id") == item_id:
                        moved_entry = self.completed_entries.pop(i)
                        self.running_entries.append(moved_entry)
                        logger.debug(
                            f"Moved entry with item_id: {item_id} from completed to running"
                        )
                        break

        logger.debug(
            f"Updated entry with item_id: {item_id} in {location} list, new status: {task_status}"
        )
        return True

    def get_total_count(self) -> dict[str, int]:
        """Get count of entries by status"""
        return {
            "completed": len(self.completed_entries),
            "running": len(self.running_entries),
            "total": len(self.completed_entries) + len(self.running_entries),
        }

    def __len__(self) -> int:
        """Return total number of entries (completed + running)"""
        return len(self.completed_entries) + len(self.running_entries)


# Alias for easier usage
SearchHistoryManager = APISearchHistoryManager
