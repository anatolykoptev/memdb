from typing import Any, ClassVar

from memdb.configs.graph_db import GraphDBConfigFactory
from memdb.graph_dbs.base import BaseGraphDB
from memdb.graph_dbs.nebular import NebulaGraphDB
from memdb.graph_dbs.neo4j import Neo4jGraphDB
from memdb.graph_dbs.neo4j_community import Neo4jCommunityGraphDB
from memdb.graph_dbs.polardb import PolarDBGraphDB
from memdb.graph_dbs.postgres import PostgresGraphDB


class GraphStoreFactory(BaseGraphDB):
    """Factory for creating graph store instances."""

    backend_to_class: ClassVar[dict[str, Any]] = {
        "neo4j": Neo4jGraphDB,
        "neo4j-community": Neo4jCommunityGraphDB,
        "nebular": NebulaGraphDB,
        "polardb": PolarDBGraphDB,
        "postgres": PostgresGraphDB,
    }

    @classmethod
    def from_config(cls, config_factory: GraphDBConfigFactory) -> BaseGraphDB:
        backend = config_factory.backend
        if backend not in cls.backend_to_class:
            raise ValueError(f"Unsupported graph database backend: {backend}")
        graph_class = cls.backend_to_class[backend]
        return graph_class(config_factory.config)
