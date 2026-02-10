import os

from pathlib import Path


MEMDB_DIR = Path(os.getenv("MEMDB_BASE_PATH", Path.cwd())) / ".memdb"
DEBUG = False

# "memdb" or "memdb.submodules" ... to filter logs from specific packages
LOG_FILTER_TREE_PREFIX = ""
