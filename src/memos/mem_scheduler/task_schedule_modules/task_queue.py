"""
Redis Queue implementation for SchedulerMessageItem objects.

This module provides a Redis-based queue implementation that can replace
the local memos_message_queue functionality in BaseScheduler.
"""

from collections import defaultdict

from memos.log import get_logger
from memos.mem_scheduler.schemas.message_schemas import ScheduleMessageItem
from memos.mem_scheduler.task_schedule_modules.local_queue import SchedulerLocalQueue
from memos.mem_scheduler.task_schedule_modules.redis_queue import SchedulerRedisQueue
from memos.mem_scheduler.utils.db_utils import get_utc_now
from memos.mem_scheduler.utils.misc_utils import group_messages_by_user_and_mem_cube


logger = get_logger(__name__)


class ScheduleTaskQueue:
    def __init__(
        self,
        use_redis_queue: bool,
        maxsize: int,
        disabled_handlers: list | None = None,
    ):
        self.use_redis_queue = use_redis_queue
        self.maxsize = maxsize

        if self.use_redis_queue:
            self.memos_message_queue = SchedulerRedisQueue(maxsize=self.maxsize)
        else:
            self.memos_message_queue = SchedulerLocalQueue(maxsize=self.maxsize)

        self.disabled_handlers = disabled_handlers

    def ack_message(
        self,
        user_id,
        mem_cube_id,
        redis_message_id,
    ) -> None:
        if not isinstance(self.memos_message_queue, SchedulerRedisQueue):
            logger.warning("ack_message is only supported for Redis queues")
            return

        self.memos_message_queue.ack_message(
            user_id=user_id,
            mem_cube_id=mem_cube_id,
            redis_message_id=redis_message_id,
        )

    def debug_mode_on(self):
        self.memos_message_queue.stream_key_prefix = (
            f"debug_mode:{self.memos_message_queue.stream_key_prefix}"
        )

    def get_stream_keys(self) -> list[str]:
        if isinstance(self.memos_message_queue, SchedulerRedisQueue):
            return self.memos_message_queue.get_stream_keys()
        else:
            return list(self.memos_message_queue.queue_streams.keys())

    def submit_messages(self, messages: ScheduleMessageItem | list[ScheduleMessageItem]):
        """Submit messages to the message queue (either local queue or Redis)."""
        if isinstance(messages, ScheduleMessageItem):
            messages = [messages]

        if len(messages) < 1:
            logger.error("Submit empty")
        elif len(messages) == 1:
            self.memos_message_queue.put(messages[0])
        else:
            user_cube_groups = group_messages_by_user_and_mem_cube(messages)

            # Process each user and mem_cube combination
            for _user_id, cube_groups in user_cube_groups.items():
                for _mem_cube_id, user_cube_msgs in cube_groups.items():
                    for message in user_cube_msgs:
                        if not isinstance(message, ScheduleMessageItem):
                            error_msg = f"Invalid message type: {type(message)}, expected ScheduleMessageItem"
                            logger.error(error_msg)
                            raise TypeError(error_msg)

                        if getattr(message, "timestamp", None) is None:
                            message.timestamp = get_utc_now()

                        if self.disabled_handlers and message.label in self.disabled_handlers:
                            logger.info(
                                f"Skipping disabled handler: {message.label} - {message.content}"
                            )
                            continue

                        self.memos_message_queue.put(message)
                        logger.info(
                            f"Submitted message to local queue: {message.label} - {message.content}"
                        )

    def get_messages(self, batch_size: int) -> list[ScheduleMessageItem]:
        # Discover all active streams via queue API
        streams: list[tuple[str, str]] = []

        stream_keys = self.get_stream_keys()
        for stream_key in stream_keys:
            try:
                parts = stream_key.split(":")
                if len(parts) >= 3:
                    user_id = parts[-2]
                    mem_cube_id = parts[-1]
                    streams.append((user_id, mem_cube_id))
            except Exception as e:
                logger.debug(f"Failed to parse stream key {stream_key}: {e}")

        if not streams:
            return []

        messages: list[ScheduleMessageItem] = []

        # Group by user: {user_id: [mem_cube_id, ...]}

        streams_by_user: dict[str, list[str]] = defaultdict(list)
        for user_id, mem_cube_id in streams:
            streams_by_user[user_id].append(mem_cube_id)

        # For each user, fairly consume up to batch_size across their streams
        for user_id, mem_cube_ids in streams_by_user.items():
            if not mem_cube_ids:
                continue

            # First pass: give each stream an equal share for this user
            for mem_cube_id in mem_cube_ids:
                fetched = self.memos_message_queue.get(
                    user_id=user_id,
                    mem_cube_id=mem_cube_id,
                    block=False,
                    batch_size=batch_size,
                )

                messages.extend(fetched)

        logger.info(
            f"Fetched {len(messages)} messages across users with per-user batch_size={batch_size}"
        )
        return messages

    def clear(self):
        self.memos_message_queue.clear()

    def qsize(self):
        return self.memos_message_queue.qsize()
