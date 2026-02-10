from unittest.mock import patch

from memdb.hello_world import (
    memdb_chend_hello_world,
    memdb_chentang_hello_world,
    memdb_dany_hello_world,
    memdb_hello_world,
    memdb_huojh_hello_world,
    memdb_niusm_hello_world,
    memdb_wanghy_hello_world,
    memdb_wangyzh_hello_world,
    memdb_yuqingchen_hello_world,
    memdb_zhaojihao_hello_world,
)


def test_memdb_hello_world_logger_called():
    """Test that the logger.info method is called and "Hello world from memdb!" is returned."""
    with patch("memdb.hello_world.logger.info") as mock_logger:
        result = memdb_hello_world()

        assert result == "Hello world from memdb!"
        mock_logger.assert_called_once_with("memdb_hello_world function called.")


def test_memdb_dany_hello_world_logger_called():
    """# What's patch for?
    Using path, we can mock a function that is called in the function we are testing.

    > For example, a new function A called function B, and function B will take a long time to run.
    > So testing function A will take a long time.
    > Using path, we can pmock a return value from B, so that we can test function A faster.
    """
    # Multiple test cases example:
    test_cases = [
        (1, "data1", "logger.info: para_1 is 1", "logger.debug: para_2 is data1", "return_value_1"),
        (2, "data2", "logger.info: para_1 is 2", "logger.debug: para_2 is data2", "return_value_2"),
        (3, "data3", "logger.info: para_1 is 3", "logger.debug: para_2 is data3", "return_value_3"),
    ]
    with (
        patch("memdb.hello_world.logger.info") as mock_logger_info,
        patch("memdb.hello_world.logger.debug") as mock_logger_debug,
    ):
        for para1, para2, expected_output_1, expected_output_2, expected_return_value in test_cases:
            result = memdb_dany_hello_world(para1, para2)

            assert result == expected_return_value
            mock_logger_info.assert_any_call(expected_output_1)
            mock_logger_debug.assert_called_once_with(expected_output_2)

            mock_logger_info.reset_mock()
            mock_logger_debug.reset_mock()


def test_memdb_chend_hello_world_logger_called():
    """Test that the logger.info method is called and "Hello world from memdb-chend!" is returned."""
    with patch("memdb.hello_world.logger.info") as mock_logger:
        result = memdb_chend_hello_world()

        assert result == "Hello world from memdb-chend!"
        mock_logger.assert_called_once_with("memdb_chend_hello_world function called.")


def test_memdb_wanghy_hello_world_logger_called():
    """Test that the logger.info method is called and "Hello world from memdb-wanghy!" is returned."""
    with patch("memdb.hello_world.logger.info") as mock_logger:
        result = memdb_wanghy_hello_world()

        assert result == "Hello world from memdb-wanghy!"
        mock_logger.assert_called_once_with("memdb_wanghy_hello_world function called.")


def test_memdb_huojh_hello_world_logger_called():
    """Test that the logger.info method is called and quicksort is okay."""
    with patch("memdb.hello_world.logger.info") as mock_logger:
        arr = [1, 7, 4, 1, 10, 9, -2]
        sorted_arr = [-2, 1, 1, 4, 7, 9, 10]
        res = memdb_huojh_hello_world(arr)

        assert all(x == y for x, y in zip(sorted_arr, res, strict=False))
        mock_logger.assert_called_with("memdb_huojh_hello_world function called.")


def test_memdb_niusm_hello_world_logger_called():
    """Test that the logger.info method is called and "Hello world from memdb-niusm!" is returned."""
    with patch("memdb.hello_world.logger.info") as mock_logger:
        result = memdb_niusm_hello_world()

        assert result == "Hello world from memdb-niusm!"
        mock_logger.assert_called_once_with("memdb_niusm_hello_world function called.")


def test_memdb_wangyzh_hello_world_logger_called():
    """Test that the logger.info method is called and "Hello world from memdb-wangyzh!" is returned."""
    with patch("memdb.hello_world.logger.info") as mock_logger:
        result = memdb_wangyzh_hello_world()

        assert result == "Hello world from memdb-wangyzh!"
        mock_logger.assert_called_once_with("memdb_wangyzh_hello_world function called.")


def test_memdb_zhaojihao_hello_world_logger_called():
    """Test that the logger.info method is called and "Hello world from memdb-zhaojihao!" is returned."""
    with patch("memdb.hello_world.logger.info") as mock_logger:
        result = memdb_zhaojihao_hello_world()

        assert result == "Hello world from memdb-zhaojihao!"
        mock_logger.assert_called_once_with("memdb_zhaojihao_hello_world function called.")


def test_memdb_yuqingchen_hello_world_logger_called():
    """Test that the logger.info method is called and "Hello world from memdb-yuqingchen!" is returned."""
    with patch("memdb.hello_world.logger.info") as mock_logger:
        result = memdb_yuqingchen_hello_world()

        assert result == "Hello world from memdb-yuqingchen!"
        mock_logger.assert_called_once_with("memdb_yuqingchen_hello_world function called.")


def test_memos_chen_tang_hello_world():
    import warnings

    from memdb.memories.textual.general import GeneralTextMemory

    # Define return values for os.getenv
    def mock_getenv(key, default=None):
        mock_values = {
            "MODEL": "mock-model-name",
            "OPENAI_API_KEY": "mock-api-key",
            "OPENAI_BASE_URL": "mock-api-url",
            "EMBEDDING_MODEL": "mock-embedding-model",
        }
        return mock_values.get(key, default)

    # Filter Pydantic serialization warnings
    with warnings.catch_warnings():
        warnings.filterwarnings("ignore", category=UserWarning, module="pydantic")
        # Use patch to mock os.getenv
        with patch("os.getenv", side_effect=mock_getenv):
            memory = memdb_chentang_hello_world()
            assert isinstance(memory, GeneralTextMemory)
