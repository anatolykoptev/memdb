"""PolarDB graph database package using Apache AGE extension."""

from memdb.graph_dbs.base import BaseGraphDB
from memdb.graph_dbs.polardb.connection import ConnectionMixin
from memdb.graph_dbs.polardb.edges import EdgeMixin
from memdb.graph_dbs.polardb.filters import FilterMixin
from memdb.graph_dbs.polardb.maintenance import MaintenanceMixin
from memdb.graph_dbs.polardb.nodes import NodeMixin
from memdb.graph_dbs.polardb.queries import QueryMixin
from memdb.graph_dbs.polardb.search import SearchMixin
from memdb.graph_dbs.polardb.traversal import TraversalMixin


class PolarDBGraphDB(
    ConnectionMixin,
    NodeMixin,
    EdgeMixin,
    TraversalMixin,
    SearchMixin,
    FilterMixin,
    QueryMixin,
    MaintenanceMixin,
    BaseGraphDB,
):
    """PolarDB-based graph database using Apache AGE extension."""
