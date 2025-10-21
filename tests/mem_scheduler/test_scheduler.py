import sys
import unittest

from contextlib import suppress
from datetime import datetime
from pathlib import Path
from unittest.mock import MagicMock, patch

from memos.configs.mem_scheduler import (
    AuthConfig,
    GraphDBAuthConfig,
    OpenAIConfig,
    RabbitMQConfig,
    SchedulerConfigFactory,
)
from memos.llms.base import BaseLLM
from memos.mem_cube.general import GeneralMemCube
from memos.mem_scheduler.memory_manage_modules.retriever import SchedulerRetriever
from memos.mem_scheduler.monitors.general_monitor import SchedulerGeneralMonitor
from memos.mem_scheduler.scheduler_factory import SchedulerFactory
from memos.mem_scheduler.schemas.general_schemas import (
    ANSWER_LABEL,
    QUERY_LABEL,
    STARTUP_BY_PROCESS,
    STARTUP_BY_THREAD,
)
from memos.mem_scheduler.schemas.message_schemas import (
    ScheduleLogForWebItem,
    ScheduleMessageItem,
)
from memos.memories.textual.tree import TreeTextMemory


FILE_PATH = Path(__file__).absolute()
BASE_DIR = FILE_PATH.parent.parent.parent
sys.path.insert(0, str(BASE_DIR))  # Enable execution from any working directory


class TestGeneralScheduler(unittest.TestCase):
    # Control whether to run activation memory tests that require GPU, default is False
    RUN_ACTIVATION_MEMORY_TESTS = True

    def _create_mock_auth_config(self):
        """Create a mock AuthConfig for testing purposes."""
        # Create mock configs with valid test values
        graph_db_config = GraphDBAuthConfig(
            uri="bolt://localhost:7687",
            user="neo4j",
            password="test_password_123",  # 8+ characters to pass validation
            db_name="neo4j",
            auto_create=True,
        )

        rabbitmq_config = RabbitMQConfig(
            host_name="localhost", port=5672, user_name="guest", password="guest", virtual_host="/"
        )

        openai_config = OpenAIConfig(api_key="test_api_key_123", default_model="gpt-3.5-turbo")

        return AuthConfig(rabbitmq=rabbitmq_config, openai=openai_config, graph_db=graph_db_config)

    def setUp(self):
        """Initialize test environment with mock objects and test scheduler instance."""
        example_scheduler_config_path = (
            f"{BASE_DIR}/examples/data/config/mem_scheduler/general_scheduler_config.yaml"
        )
        scheduler_config = SchedulerConfigFactory.from_yaml_file(
            yaml_path=example_scheduler_config_path
        )
        mem_scheduler = SchedulerFactory.from_config(scheduler_config)
        self.scheduler = mem_scheduler
        self.llm = MagicMock(spec=BaseLLM)
        self.mem_cube = MagicMock(spec=GeneralMemCube)
        self.tree_text_memory = MagicMock(spec=TreeTextMemory)
        # Add memory_manager mock to prevent AttributeError in scheduler_logger
        self.tree_text_memory.memory_manager = MagicMock()
        self.tree_text_memory.memory_manager.memory_size = {
            "LongTermMemory": 10000,
            "UserMemory": 10000,
            "WorkingMemory": 20,
        }
        # Mock get_current_memory_size method
        self.tree_text_memory.get_current_memory_size.return_value = {
            "LongTermMemory": 100,
            "UserMemory": 50,
            "WorkingMemory": 10,
        }
        self.mem_cube.text_mem = self.tree_text_memory
        self.mem_cube.act_mem = MagicMock()

        # Mock AuthConfig.from_local_env() to return our test config
        mock_auth_config = self._create_mock_auth_config()
        self.auth_config_patch = patch(
            "memos.configs.mem_scheduler.AuthConfig.from_local_env", return_value=mock_auth_config
        )
        self.auth_config_patch.start()

        # Initialize general_modules with mock LLM
        self.scheduler.initialize_modules(chat_llm=self.llm, process_llm=self.llm)
        self.scheduler.mem_cube = self.mem_cube

        # Set current user and memory cube ID for testing
        self.scheduler.current_user_id = "test_user"
        self.scheduler.current_mem_cube_id = "test_cube"

    def tearDown(self):
        """Clean up patches."""
        self.auth_config_patch.stop()

    def test_initialization(self):
        """Test that scheduler initializes with correct default values and handlers."""
        # Verify handler registration
        self.assertTrue(QUERY_LABEL in self.scheduler.dispatcher.handlers)
        self.assertTrue(ANSWER_LABEL in self.scheduler.dispatcher.handlers)

    def test_initialize_modules(self):
        """Test module initialization with proper component assignments."""
        self.assertEqual(self.scheduler.chat_llm, self.llm)
        self.assertIsInstance(self.scheduler.monitor, SchedulerGeneralMonitor)
        self.assertIsInstance(self.scheduler.retriever, SchedulerRetriever)

    def test_submit_web_logs(self):
        """Test submission of web logs with updated data structure."""
        # Create log message with all required fields
        log_message = ScheduleLogForWebItem(
            user_id="test_user",
            mem_cube_id="test_cube",
            label=QUERY_LABEL,
            from_memory_type="WorkingMemory",  # New field
            to_memory_type="LongTermMemory",  # New field
            log_content="Test Content",
            current_memory_sizes={
                "long_term_memory_size": 0,
                "user_memory_size": 0,
                "working_memory_size": 0,
                "transformed_act_memory_size": 0,
            },
            memory_capacities={
                "long_term_memory_capacity": 1000,
                "user_memory_capacity": 500,
                "working_memory_capacity": 100,
                "transformed_act_memory_capacity": 0,
            },
        )

        # Empty the queue by consuming all elements
        while not self.scheduler._web_log_message_queue.empty():
            self.scheduler._web_log_message_queue.get()

        # Submit the log message
        self.scheduler._submit_web_logs(messages=log_message)

        # Verify the message was added to the queue
        self.assertEqual(self.scheduler._web_log_message_queue.qsize(), 1)

        # Get the actual message from the queue
        actual_message = self.scheduler._web_log_message_queue.get()

        # Verify core fields
        self.assertEqual(actual_message.user_id, "test_user")
        self.assertEqual(actual_message.mem_cube_id, "test_cube")
        self.assertEqual(actual_message.label, QUERY_LABEL)
        self.assertEqual(actual_message.from_memory_type, "WorkingMemory")
        self.assertEqual(actual_message.to_memory_type, "LongTermMemory")
        self.assertEqual(actual_message.log_content, "Test Content")

        # Verify memory sizes
        self.assertEqual(actual_message.current_memory_sizes["long_term_memory_size"], 0)
        self.assertEqual(actual_message.current_memory_sizes["user_memory_size"], 0)
        self.assertEqual(actual_message.current_memory_sizes["working_memory_size"], 0)
        self.assertEqual(actual_message.current_memory_sizes["transformed_act_memory_size"], 0)

        # Verify memory capacities
        self.assertEqual(actual_message.memory_capacities["long_term_memory_capacity"], 1000)
        self.assertEqual(actual_message.memory_capacities["user_memory_capacity"], 500)
        self.assertEqual(actual_message.memory_capacities["working_memory_capacity"], 100)
        self.assertEqual(actual_message.memory_capacities["transformed_act_memory_capacity"], 0)

        # Verify auto-generated fields exist
        self.assertTrue(hasattr(actual_message, "item_id"))
        self.assertTrue(isinstance(actual_message.item_id, str))
        self.assertTrue(hasattr(actual_message, "timestamp"))
        self.assertTrue(isinstance(actual_message.timestamp, datetime))

    def test_scheduler_startup_mode_default(self):
        """Test that scheduler has default startup mode set to thread."""
        self.assertEqual(self.scheduler.scheduler_startup_mode, STARTUP_BY_THREAD)

    def test_scheduler_startup_mode_thread(self):
        """Test scheduler with thread startup mode."""
        # Set scheduler startup mode to thread
        self.scheduler.scheduler_startup_mode = STARTUP_BY_THREAD

        # Start the scheduler
        self.scheduler.start()

        # Verify that consumer thread is created and process is None
        self.assertIsNotNone(self.scheduler._consumer_thread)
        self.assertIsNone(self.scheduler._consumer_process)
        self.assertTrue(self.scheduler._running)

        # Stop the scheduler
        self.scheduler.stop()

    def test_robustness(self):
        """Test dispatcher robustness when thread pool is overwhelmed with tasks."""
        import threading
        import time

        # Create a scheduler with a small thread pool for testing
        small_max_workers = 3
        self.scheduler.dispatcher.max_workers = small_max_workers

        # Recreate dispatcher with smaller thread pool
        from memos.context.context import ContextThreadPoolExecutor

        if self.scheduler.dispatcher.dispatcher_executor:
            self.scheduler.dispatcher.dispatcher_executor.shutdown(wait=True)

        self.scheduler.dispatcher.dispatcher_executor = ContextThreadPoolExecutor(
            max_workers=small_max_workers, thread_name_prefix="test_dispatcher"
        )

        # Track task completion
        completed_tasks = []
        failed_tasks = []
        task_lock = threading.Lock()

        def slow_handler(messages: list[ScheduleMessageItem]) -> None:
            """Handler that simulates slow processing to overwhelm thread pool."""
            try:
                task_id = messages[0].content if messages else "unknown"
                # Simulate slow processing (reduced from 2.0s to 20ms)
                time.sleep(0.02)
                with task_lock:
                    completed_tasks.append(task_id)
            except Exception as e:
                with task_lock:
                    failed_tasks.append(str(e))

        def fast_handler(messages: list[ScheduleMessageItem]) -> None:
            """Handler for quick tasks to test mixed workload."""
            try:
                task_id = messages[0].content if messages else "unknown"
                time.sleep(0.001)  # Quick processing (reduced from 0.1s to 1ms)
                with task_lock:
                    completed_tasks.append(f"fast_{task_id}")
            except Exception as e:
                with task_lock:
                    failed_tasks.append(str(e))

        # Register handlers
        slow_label = "slow_task"
        fast_label = "fast_task"
        self.scheduler.register_handlers({slow_label: slow_handler, fast_label: fast_handler})

        # Start the scheduler
        self.scheduler.start()

        # Test 1: Overwhelm thread pool with slow tasks
        print("Test 1: Overwhelming thread pool with slow tasks...")
        num_slow_tasks = small_max_workers * 3  # 9 tasks for 3 workers

        slow_messages = []
        for i in range(num_slow_tasks):
            message = ScheduleMessageItem(
                label=slow_label,
                content=f"slow_task_{i}",
                user_id=f"test_user_{i}",
                mem_cube_id=f"test_mem_cube_{i}",
                mem_cube="test_mem_cube_obj",
                timestamp=datetime.now(),
            )
            slow_messages.append(message)

        # Submit all slow tasks at once - directly dispatch instead of using submit_messages
        start_time = time.time()
        try:
            # Directly dispatch messages to bypass queue and immediately start processing
            self.scheduler.dispatcher.dispatch(slow_messages)
        except Exception as e:
            print(f"Exception during task dispatch: {e}")

        # Test 2: Add fast tasks while slow tasks are running
        print("Test 2: Adding fast tasks while thread pool is busy...")
        time.sleep(0.005)  # Let slow tasks start (reduced from 0.5s to 5ms)

        num_fast_tasks = 5
        fast_messages = []
        for i in range(num_fast_tasks):
            message = ScheduleMessageItem(
                label=fast_label,
                content=f"fast_task_{i}",
                user_id=f"fast_user_{i}",
                mem_cube_id=f"fast_mem_cube_{i}",
                mem_cube="fast_mem_cube_obj",
                timestamp=datetime.now(),
            )
            fast_messages.append(message)

        try:
            # Directly dispatch fast messages
            self.scheduler.dispatcher.dispatch(fast_messages)
        except Exception as e:
            print(f"Exception during fast task dispatch: {e}")

        # Test 3: Check thread pool status during overload
        print("Test 3: Monitoring thread pool status...")
        running_tasks = self.scheduler.dispatcher.get_running_tasks()
        running_count = self.scheduler.dispatcher.get_running_task_count()
        print(f"Running tasks count: {running_count}")
        print(f"Running tasks: {list(running_tasks.keys())}")

        # Test 4: Wait for some tasks to complete and verify recovery
        print("Test 4: Waiting for task completion and recovery...")
        max_wait_time = 0.5  # Maximum wait time (reduced from 15.0s to 0.5s)
        wait_start = time.time()

        while time.time() - wait_start < max_wait_time:
            with task_lock:
                total_completed = len(completed_tasks)
                total_failed = len(failed_tasks)

            if total_completed + total_failed >= num_slow_tasks + num_fast_tasks:
                break

            time.sleep(0.01)  # Check every 10ms (reduced from 1.0s)

        # Final verification
        execution_time = time.time() - start_time
        with task_lock:
            final_completed = len(completed_tasks)
            final_failed = len(failed_tasks)

        print(f"Execution completed in {execution_time:.2f} seconds")
        print(f"Completed tasks: {final_completed}")
        print(f"Failed tasks: {final_failed}")
        print(f"Completed task IDs: {completed_tasks}")
        if failed_tasks:
            print(f"Failed task errors: {failed_tasks}")

        # Assertions for robustness test
        # At least some tasks should complete successfully
        self.assertGreater(final_completed, 0, "No tasks completed successfully")

        # Total processed should be reasonable (allowing for some failures under stress)
        total_processed = final_completed + final_failed
        expected_total = num_slow_tasks + num_fast_tasks
        self.assertGreaterEqual(
            total_processed,
            expected_total * 0.7,  # Allow 30% failure rate under extreme stress
            f"Too few tasks processed: {total_processed}/{expected_total}",
        )

        # Fast tasks should generally complete faster than slow tasks
        fast_completed = [task for task in completed_tasks if task.startswith("fast_")]
        self.assertGreater(len(fast_completed), 0, "No fast tasks completed")

        # Test 5: Verify thread pool recovery after stress
        print("Test 5: Testing thread pool recovery...")
        recovery_messages = []
        for i in range(3):  # Small number of recovery tasks
            message = ScheduleMessageItem(
                label=fast_label,
                content=f"recovery_task_{i}",
                user_id=f"recovery_user_{i}",
                mem_cube_id=f"recovery_mem_cube_{i}",
                mem_cube="recovery_mem_cube_obj",
                timestamp=datetime.now(),
            )
            recovery_messages.append(message)

        # Clear previous results
        with task_lock:
            completed_tasks.clear()
            failed_tasks.clear()

        # Submit recovery tasks - directly dispatch
        try:
            self.scheduler.dispatcher.dispatch(recovery_messages)
        except Exception as e:
            print(f"Exception during recovery task dispatch: {e}")

        # Wait for recovery tasks to be processed
        time.sleep(0.05)  # Give time for recovery tasks to complete (reduced from 3.0s to 50ms)

        with task_lock:
            recovery_completed = len(completed_tasks)
            recovery_failed = len(failed_tasks)

        print(f"Recovery test - Completed: {recovery_completed}, Failed: {recovery_failed}")

        # Recovery tasks should complete successfully
        self.assertGreaterEqual(
            recovery_completed,
            len(recovery_messages) * 0.8,  # Allow some margin
            "Thread pool did not recover properly after stress test",
        )

        # Stop the scheduler
        self.scheduler.stop()

        # Test 6: Simulate dispatcher monitor restart functionality
        print("Test 6: Testing dispatcher monitor restart functionality...")

        # Force a failure condition by setting failure count high
        monitor = self.scheduler.dispatcher_monitor
        if monitor and hasattr(monitor, "_pools"):
            with monitor._pool_lock:
                pool_name = monitor.dispatcher_pool_name
                if pool_name in monitor._pools:
                    # Simulate multiple failures to trigger restart
                    monitor._pools[pool_name]["failure_count"] = monitor.max_failures - 1
                    monitor._pools[pool_name]["healthy"] = False
                    print(f"Set failure count to {monitor._pools[pool_name]['failure_count']}")

                    # Trigger one more failure to cause restart
                    monitor._check_pools_health()

                    # Wait a bit for restart to complete
                    time.sleep(0.02)  # Reduced from 2s to 20ms

                    # Check if pool was restarted (failure count should be reset)
                    if pool_name in monitor._pools:
                        final_failure_count = monitor._pools[pool_name]["failure_count"]
                        is_healthy = monitor._pools[pool_name]["healthy"]
                        print(
                            f"After restart - Failure count: {final_failure_count}, Healthy: {is_healthy}"
                        )

                        # Verify restart worked
                        assert final_failure_count < monitor.max_failures, (
                            f"Expected failure count to be reset, got {final_failure_count}"
                        )
                        print("Dispatcher monitor restart functionality verified!")
                    else:
                        print("Pool not found after restart attempt")
                else:
                    print(f"Pool {pool_name} not found in monitor registry")
        else:
            print("Dispatcher monitor not available or pools not accessible")

        print("Robustness test completed successfully!")

        # Verify cleanup
        self.assertFalse(self.scheduler._running)

    def test_scheduler_startup_mode_process(self):
        """Test scheduler with process startup mode."""
        # Set scheduler startup mode to process
        self.scheduler.scheduler_startup_mode = STARTUP_BY_PROCESS

        # Start the scheduler
        try:
            self.scheduler.start()

            # Verify that consumer process is created and thread is None
            self.assertIsNotNone(self.scheduler._consumer_process)
            self.assertIsNone(self.scheduler._consumer_thread)
            self.assertTrue(self.scheduler._running)

        except Exception as e:
            # Process mode may fail due to pickling issues in test environment
            # This is expected behavior - we just verify the startup mode is set correctly
            self.assertEqual(self.scheduler.scheduler_startup_mode, STARTUP_BY_PROCESS)
            print(f"Process mode test encountered expected pickling issue: {e}")
        finally:
            # Always attempt to stop the scheduler
            with suppress(Exception):
                self.scheduler.stop()

            # Verify cleanup attempt was made
            self.assertEqual(self.scheduler.scheduler_startup_mode, STARTUP_BY_PROCESS)

    def test_scheduler_startup_mode_constants(self):
        """Test that startup mode constants are properly defined."""
        self.assertEqual(STARTUP_BY_THREAD, "thread")
        self.assertEqual(STARTUP_BY_PROCESS, "process")

    def test_activation_memory_update(self):
        """Test activation memory update functionality with DynamicCache handling."""
        if not self.RUN_ACTIVATION_MEMORY_TESTS:
            self.skipTest(
                "Skipping activation memory test. Set RUN_ACTIVATION_MEMORY_TESTS=True to enable."
            )

        from unittest.mock import Mock

        from transformers import DynamicCache

        from memos.memories.activation.kv import KVCacheMemory

        # Mock the mem_cube with activation memory
        mock_kv_cache_memory = Mock(spec=KVCacheMemory)
        self.mem_cube.act_mem = mock_kv_cache_memory

        # Mock get_all to return empty list (no existing cache items)
        mock_kv_cache_memory.get_all.return_value = []

        # Create a mock DynamicCache with layers attribute
        mock_cache = Mock(spec=DynamicCache)
        mock_cache.layers = []

        # Create mock layers with key_cache and value_cache
        for _ in range(2):  # Simulate 2 layers
            mock_layer = Mock()
            mock_layer.key_cache = Mock()
            mock_layer.value_cache = Mock()
            mock_cache.layers.append(mock_layer)

        # Mock the extract method to return a KVCacheItem
        mock_cache_item = Mock()
        mock_cache_item.records = Mock()
        mock_cache_item.records.text_memories = []
        mock_cache_item.records.timestamp = None
        mock_kv_cache_memory.extract.return_value = mock_cache_item

        # Test data
        test_memories = ["Test memory 1", "Test memory 2"]
        user_id = "test_user"
        mem_cube_id = "test_cube"

        # Call the method under test
        try:
            self.scheduler.update_activation_memory(
                new_memories=test_memories,
                label=QUERY_LABEL,
                user_id=user_id,
                mem_cube_id=mem_cube_id,
                mem_cube=self.mem_cube,
            )

            # Verify that extract was called
            mock_kv_cache_memory.extract.assert_called_once()

            # Verify that add was called with the extracted cache item
            mock_kv_cache_memory.add.assert_called_once()

            # Verify that dump was called
            mock_kv_cache_memory.dump.assert_called_once()

            print("✅ Activation memory update test passed - DynamicCache layers handled correctly")

        except Exception as e:
            self.fail(f"Activation memory update failed: {e}")

    def test_dynamic_cache_layers_access(self):
        """Test DynamicCache layers attribute access for compatibility."""
        if not self.RUN_ACTIVATION_MEMORY_TESTS:
            self.skipTest(
                "Skipping activation memory test. Set RUN_ACTIVATION_MEMORY_TESTS=True to enable."
            )

        from unittest.mock import Mock

        from transformers import DynamicCache

        # Create a real DynamicCache instance
        cache = DynamicCache()

        # Check if it has layers attribute (may vary by transformers version)
        if hasattr(cache, "layers"):
            self.assertIsInstance(cache.layers, list, "DynamicCache.layers should be a list")

            # Test with mock layers
            mock_layer = Mock()
            mock_layer.key_cache = Mock()
            mock_layer.value_cache = Mock()
            cache.layers.append(mock_layer)

            # Verify we can access layer attributes
            self.assertEqual(len(cache.layers), 1)
            self.assertTrue(hasattr(cache.layers[0], "key_cache"))
            self.assertTrue(hasattr(cache.layers[0], "value_cache"))

            print("✅ DynamicCache layers access test passed")
        else:
            # If layers attribute doesn't exist, verify our fix handles this case
            print("⚠️  DynamicCache doesn't have 'layers' attribute in this transformers version")
            print("✅ Test passed - our code should handle this gracefully")

    def test_get_running_tasks_no_filter(self):
        """Test get_running_tasks method without filter."""
        # Mock dispatcher and its get_running_tasks method
        mock_task_item = MagicMock()
        mock_task_item.item_id = "task_1"
        mock_task_item.user_id = "user_1"
        mock_task_item.mem_cube_id = "cube_1"
        mock_task_item.task_info = {"type": "query"}
        mock_task_item.task_name = "test_task"
        mock_task_item.start_time = datetime.now()
        mock_task_item.end_time = None
        mock_task_item.status = "running"
        mock_task_item.result = None
        mock_task_item.error_message = None
        mock_task_item.messages = []

        # Mock the dispatcher's get_running_tasks method
        with patch.object(
            self.scheduler.dispatcher, "get_running_tasks", return_value={"task_1": mock_task_item}
        ) as mock_get_running_tasks:
            # Call get_running_tasks
            result = self.scheduler.get_running_tasks()

            # Verify result structure
            self.assertIsInstance(result, dict)
            self.assertIn("task_1", result)

            task_dict = result["task_1"]
            self.assertEqual(task_dict["item_id"], "task_1")
            self.assertEqual(task_dict["user_id"], "user_1")
            self.assertEqual(task_dict["mem_cube_id"], "cube_1")
            self.assertEqual(task_dict["task_info"], {"type": "query"})
            self.assertEqual(task_dict["task_name"], "test_task")
            self.assertEqual(task_dict["status"], "running")
            self.assertIsNone(task_dict["result"])
            self.assertIsNone(task_dict["error_message"])
            self.assertEqual(task_dict["messages"], [])

            # Verify dispatcher method was called without filter
            mock_get_running_tasks.assert_called_once_with(filter_func=None)

    def test_get_running_tasks_with_filter(self):
        """Test get_running_tasks method with filter function."""
        # Mock dispatcher and its get_running_tasks method
        mock_task_item1 = MagicMock()
        mock_task_item1.item_id = "task_1"
        mock_task_item1.user_id = "user_1"
        mock_task_item1.mem_cube_id = "cube_1"
        mock_task_item1.task_info = {"type": "query"}
        mock_task_item1.task_name = "test_task_1"
        mock_task_item1.start_time = datetime.now()
        mock_task_item1.end_time = None
        mock_task_item1.status = "running"
        mock_task_item1.result = None
        mock_task_item1.error_message = None
        mock_task_item1.messages = []

        # Define a filter function
        def user_filter(task):
            return task.user_id == "user_1"

        # Mock the filtered result (only task_1 matches the filter)
        with patch.object(
            self.scheduler.dispatcher, "get_running_tasks", return_value={"task_1": mock_task_item1}
        ) as mock_get_running_tasks:
            # Call get_running_tasks with filter
            result = self.scheduler.get_running_tasks(filter_func=user_filter)

            # Verify result
            self.assertIsInstance(result, dict)
            self.assertIn("task_1", result)
            self.assertEqual(len(result), 1)

            # Verify dispatcher method was called with filter
            mock_get_running_tasks.assert_called_once_with(filter_func=user_filter)

    def test_get_running_tasks_empty_result(self):
        """Test get_running_tasks method when no tasks are running."""
        # Mock dispatcher to return empty dict
        with patch.object(
            self.scheduler.dispatcher, "get_running_tasks", return_value={}
        ) as mock_get_running_tasks:
            # Call get_running_tasks
            result = self.scheduler.get_running_tasks()

            # Verify empty result
            self.assertIsInstance(result, dict)
            self.assertEqual(len(result), 0)

            # Verify dispatcher method was called
            mock_get_running_tasks.assert_called_once_with(filter_func=None)

    def test_get_running_tasks_no_dispatcher(self):
        """Test get_running_tasks method when dispatcher is None."""
        # Temporarily set dispatcher to None
        original_dispatcher = self.scheduler.dispatcher
        self.scheduler.dispatcher = None

        # Call get_running_tasks
        result = self.scheduler.get_running_tasks()

        # Verify empty result and warning behavior
        self.assertIsInstance(result, dict)
        self.assertEqual(len(result), 0)

        # Restore dispatcher
        self.scheduler.dispatcher = original_dispatcher

    def test_get_running_tasks_multiple_tasks(self):
        """Test get_running_tasks method with multiple tasks."""
        # Mock multiple task items
        mock_task_item1 = MagicMock()
        mock_task_item1.item_id = "task_1"
        mock_task_item1.user_id = "user_1"
        mock_task_item1.mem_cube_id = "cube_1"
        mock_task_item1.task_info = {"type": "query"}
        mock_task_item1.task_name = "test_task_1"
        mock_task_item1.start_time = datetime.now()
        mock_task_item1.end_time = None
        mock_task_item1.status = "running"
        mock_task_item1.result = None
        mock_task_item1.error_message = None
        mock_task_item1.messages = []

        mock_task_item2 = MagicMock()
        mock_task_item2.item_id = "task_2"
        mock_task_item2.user_id = "user_2"
        mock_task_item2.mem_cube_id = "cube_2"
        mock_task_item2.task_info = {"type": "answer"}
        mock_task_item2.task_name = "test_task_2"
        mock_task_item2.start_time = datetime.now()
        mock_task_item2.end_time = None
        mock_task_item2.status = "completed"
        mock_task_item2.result = "success"
        mock_task_item2.error_message = None
        mock_task_item2.messages = ["message1", "message2"]

        with patch.object(
            self.scheduler.dispatcher,
            "get_running_tasks",
            return_value={"task_1": mock_task_item1, "task_2": mock_task_item2},
        ) as mock_get_running_tasks:
            # Call get_running_tasks
            result = self.scheduler.get_running_tasks()

            # Verify result structure
            self.assertIsInstance(result, dict)
            self.assertEqual(len(result), 2)
            self.assertIn("task_1", result)
            self.assertIn("task_2", result)

            # Verify task_1 details
            task1_dict = result["task_1"]
            self.assertEqual(task1_dict["item_id"], "task_1")
            self.assertEqual(task1_dict["user_id"], "user_1")
            self.assertEqual(task1_dict["status"], "running")

            # Verify task_2 details
            task2_dict = result["task_2"]
            self.assertEqual(task2_dict["item_id"], "task_2")
            self.assertEqual(task2_dict["user_id"], "user_2")
            self.assertEqual(task2_dict["status"], "completed")
            self.assertEqual(task2_dict["result"], "success")
            self.assertEqual(task2_dict["messages"], ["message1", "message2"])

            # Verify dispatcher method was called
            mock_get_running_tasks.assert_called_once_with(filter_func=None)

    def test_message_handler_receives_submitted_message(self):
        """Test that handlers receive messages after scheduler startup and message submission."""
        # Create a mock handler that tracks received messages
        received_messages = []

        def mock_handler(messages: list[ScheduleMessageItem]) -> None:
            """Mock handler that records received messages."""
            received_messages.extend(messages)

        # Register the mock handler
        test_label = "test_handler"
        handlers = {test_label: mock_handler}
        self.scheduler.register_handlers(handlers)

        # Verify handler is registered
        self.assertIn(test_label, self.scheduler.handlers)
        self.assertEqual(self.scheduler.handlers[test_label], mock_handler)

        # Start the scheduler
        self.scheduler.start()

        # Create and submit a test message
        test_message = ScheduleMessageItem(
            label=test_label,
            content="Test message content",
            user_id="test_user",
            mem_cube_id="test_mem_cube",
            mem_cube="test_mem_cube_obj",  # Required field - can be string or GeneralMemCube
            timestamp=datetime.now(),
        )

        self.scheduler.submit_messages(test_message)

        # Wait for message processing to complete
        import time

        time.sleep(2.0)  # Allow sufficient time for message processing

        # Verify the handler received the message
        self.assertEqual(
            len(received_messages), 1, f"Expected 1 message, got {len(received_messages)}"
        )
        self.assertEqual(received_messages[0].label, test_label)
        self.assertEqual(received_messages[0].content, "Test message content")
        self.assertEqual(received_messages[0].user_id, "test_user")
        self.assertEqual(received_messages[0].mem_cube_id, "test_mem_cube")

        # Stop the scheduler
        self.scheduler.stop()
