from memos.log import get_logger
from memos.utils import timed

logger = get_logger(__name__)


class SchemaMixin:
    """Mixin for schema and extension management."""

    def _ensure_database_exists(self):
        """Create database if it doesn't exist."""
        try:
            # For PostgreSQL/PolarDB, we need to connect to a default database first
            # This is a simplified implementation - in production you might want to handle this differently
            logger.info(f"Using database '{self.db_name}'")
        except Exception as e:
            logger.error(f"Failed to access database '{self.db_name}': {e}")
            raise

    @timed
    def _create_graph(self):
        """Create PostgreSQL schema and table for graph storage."""
        # Get a connection from the pool
        conn = None
        try:
            conn = self._get_connection()
            with conn.cursor() as cursor:
                # Create schema if it doesn't exist
                cursor.execute(f'CREATE SCHEMA IF NOT EXISTS "{self.db_name}_graph";')
                logger.info(f"Schema '{self.db_name}_graph' ensured.")

                # Create Memory table if it doesn't exist
                cursor.execute(f"""
                    CREATE TABLE IF NOT EXISTS "{self.db_name}_graph"."Memory" (
                        id TEXT PRIMARY KEY,
                        properties JSONB NOT NULL,
                        created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
                        updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
                    );
                """)
                logger.info(f"Memory table created in schema '{self.db_name}_graph'.")

                # Add embedding column if it doesn't exist (using JSONB for compatibility)
                try:
                    cursor.execute(f"""
                        ALTER TABLE "{self.db_name}_graph"."Memory"
                        ADD COLUMN IF NOT EXISTS embedding JSONB;
                    """)
                    logger.info("Embedding column added to Memory table.")
                except Exception as e:
                    logger.warning(f"Failed to add embedding column: {e}")

                # Create indexes
                cursor.execute(f"""
                    CREATE INDEX IF NOT EXISTS idx_memory_properties
                    ON "{self.db_name}_graph"."Memory" USING GIN (properties);
                """)

                # Create vector index for embedding field
                try:
                    cursor.execute(f"""
                        CREATE INDEX IF NOT EXISTS idx_memory_embedding
                        ON "{self.db_name}_graph"."Memory" USING ivfflat (embedding vector_cosine_ops)
                        WITH (lists = 100);
                    """)
                    logger.info("Vector index created for Memory table.")
                except Exception as e:
                    logger.warning(f"Vector index creation failed (might not be supported): {e}")

                # Create tsvector column and GIN index for fulltext search
                try:
                    cursor.execute(f"""
                        ALTER TABLE "{self.db_name}_graph"."Memory"
                        ADD COLUMN IF NOT EXISTS properties_tsvector_zh tsvector;
                    """)

                    # Create or replace trigger function for auto-updating tsvector
                    cursor.execute(f"""
                        CREATE OR REPLACE FUNCTION "{self.db_name}_graph".update_tsvector_zh()
                        RETURNS trigger AS $$
                        BEGIN
                            NEW.properties_tsvector_zh :=
                                to_tsvector('simple', COALESCE(NEW.properties->>'memory', ''));
                            RETURN NEW;
                        END;
                        $$ LANGUAGE plpgsql;
                    """)

                    # Create trigger (drop first to avoid duplicate)
                    cursor.execute(f"""
                        DROP TRIGGER IF EXISTS trg_update_tsvector_zh
                        ON "{self.db_name}_graph"."Memory";
                    """)
                    cursor.execute(f"""
                        CREATE TRIGGER trg_update_tsvector_zh
                        BEFORE INSERT OR UPDATE ON "{self.db_name}_graph"."Memory"
                        FOR EACH ROW
                        EXECUTE FUNCTION "{self.db_name}_graph".update_tsvector_zh();
                    """)

                    # Create GIN index on the tsvector column
                    cursor.execute(f"""
                        CREATE INDEX IF NOT EXISTS idx_memory_tsvector_zh
                        ON "{self.db_name}_graph"."Memory"
                        USING GIN (properties_tsvector_zh);
                    """)

                    # Backfill existing rows that have NULL tsvector
                    cursor.execute(f"""
                        UPDATE "{self.db_name}_graph"."Memory"
                        SET properties_tsvector_zh =
                            to_tsvector('simple', COALESCE(properties->>'memory', ''))
                        WHERE properties_tsvector_zh IS NULL;
                    """)

                    logger.info("Fulltext search (tsvector + GIN index) created for Memory table.")
                except Exception as e:
                    logger.warning(f"Fulltext search setup failed (non-fatal): {e}")

                logger.info("Indexes created for Memory table.")

        except Exception as e:
            logger.error(f"Failed to create graph schema: {e}")
            raise e
        finally:
            self._return_connection(conn)

    def create_index(
        self,
        label: str = "Memory",
        vector_property: str = "embedding",
        dimensions: int = 1024,
        index_name: str = "memory_vector_index",
    ) -> None:
        """
        Create indexes for embedding and other fields.
        Note: This creates PostgreSQL indexes on the underlying tables.
        """
        # Get a connection from the pool
        conn = None
        try:
            conn = self._get_connection()
            with conn.cursor() as cursor:
                # Create indexes on the underlying PostgreSQL tables
                # Apache AGE stores data in regular PostgreSQL tables
                cursor.execute(f"""
                    CREATE INDEX IF NOT EXISTS idx_memory_properties
                    ON "{self.db_name}_graph"."Memory" USING GIN (properties);
                """)

                # Try to create vector index, but don't fail if it doesn't work
                try:
                    cursor.execute(f"""
                        CREATE INDEX IF NOT EXISTS idx_memory_embedding
                        ON "{self.db_name}_graph"."Memory" USING ivfflat (embedding vector_cosine_ops);
                    """)
                except Exception as ve:
                    logger.warning(f"Vector index creation failed (might not be supported): {ve}")

                logger.debug("Indexes created successfully.")
        except Exception as e:
            logger.warning(f"Failed to create indexes: {e}")
        finally:
            self._return_connection(conn)

    @timed
    def create_extension(self):
        extensions = [("polar_age", "Graph engine"), ("vector", "Vector engine")]
        # Get a connection from the pool
        conn = None
        try:
            conn = self._get_connection()
            with conn.cursor() as cursor:
                # Ensure in the correct database context
                cursor.execute("SELECT current_database();")
                current_db = cursor.fetchone()[0]
                logger.info(f"Current database context: {current_db}")

                for ext_name, ext_desc in extensions:
                    try:
                        cursor.execute(f"create extension if not exists {ext_name};")
                        logger.info(f"Extension '{ext_name}' ({ext_desc}) ensured.")
                    except Exception as e:
                        if "already exists" in str(e):
                            logger.info(f"Extension '{ext_name}' ({ext_desc}) already exists.")
                        else:
                            logger.warning(
                                f"Failed to create extension '{ext_name}' ({ext_desc}): {e}"
                            )
                            logger.error(
                                f"Failed to create extension '{ext_name}': {e}", exc_info=True
                            )
        except Exception as e:
            logger.warning(f"Failed to access database context: {e}")
            logger.error(f"Failed to access database context: {e}", exc_info=True)
        finally:
            self._return_connection(conn)

    @timed
    def create_graph(self):
        # Get a connection from the pool
        conn = None
        try:
            conn = self._get_connection()
            with conn.cursor() as cursor:
                cursor.execute(f"""
                    SELECT COUNT(*) FROM ag_catalog.ag_graph
                    WHERE name = '{self.db_name}_graph';
                """)
                graph_exists = cursor.fetchone()[0] > 0

                if graph_exists:
                    logger.info(f"Graph '{self.db_name}_graph' already exists.")
                else:
                    cursor.execute(f"select create_graph('{self.db_name}_graph');")
                    logger.info(f"Graph database '{self.db_name}_graph' created.")
        except Exception as e:
            logger.warning(f"Failed to create graph '{self.db_name}_graph': {e}")
            logger.error(f"Failed to create graph '{self.db_name}_graph': {e}", exc_info=True)
        finally:
            self._return_connection(conn)
