.PHONY: test test-migrations-fresh-db eval-locomo eval-locomo-full

install:
	poetry install --extras all --with dev --with test
	poetry run pre-commit install --install-hooks

clean:
	rm -rf .memdb
	rm -rf .pytest_cache
	rm -rf .ruff_cache
	rm -rf tmp

test:
	poetry run pytest tests

format:
	poetry run ruff check --fix
	poetry run ruff format

pre_commit:
	poetry run pre-commit run -a

serve:
	poetry run uvicorn memdb.api.start_api:app

openapi:
	poetry run memdb export_openapi --output docs/openapi.json

test-migrations-fresh-db:
	bash memdb-go/scripts/test-migrations-fresh-db.sh

# LoCoMo evaluation harness — retrieval quality measurement for memdb-go.
# Requires memdb-go stack to be running (MEMDB_URL, default localhost:8080).
# See evaluation/locomo/README.md.
eval-locomo:
	bash evaluation/locomo/run.sh

eval-locomo-full:
	LOCOMO_FULL=1 bash evaluation/locomo/run.sh
