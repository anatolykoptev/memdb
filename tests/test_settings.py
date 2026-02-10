from memdb.settings import (
    DEBUG,
    MEMDB_DIR,
)


def test_memdb_dir():
    """Test if the MEMDB_DIR is created correctly."""
    assert MEMDB_DIR.is_dir()
    assert MEMDB_DIR.name == ".memdb"


def test_debug():
    """Test if the DEBUG setting is set correctly."""
    assert DEBUG in [True, False]
