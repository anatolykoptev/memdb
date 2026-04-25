.PHONY: help build test lint vet eval-locomo eval-locomo-full test-migrations-fresh-db clean

GO := GOWORK=off go
MEMDB_GO := memdb-go

help:
	@echo "MemDB make targets:"
	@echo "  build                       — go build ./... in memdb-go"
	@echo "  test                        — go test ./... in memdb-go"
	@echo "  lint                        — golangci-lint run in memdb-go"
	@echo "  vet                         — go vet ./... in memdb-go"
	@echo "  test-migrations-fresh-db    — fresh-DB integration test"
	@echo "  eval-locomo                 — LoCoMo retrieval benchmark (sample)"
	@echo "  eval-locomo-full            — LoCoMo retrieval benchmark (full)"
	@echo "  clean                       — remove tmp + caches"

build:
	cd $(MEMDB_GO) && $(GO) build ./...

test:
	cd $(MEMDB_GO) && $(GO) test ./...

lint:
	cd $(MEMDB_GO) && golangci-lint run

vet:
	cd $(MEMDB_GO) && $(GO) vet ./...

test-migrations-fresh-db:
	bash $(MEMDB_GO)/scripts/test-migrations-fresh-db.sh

eval-locomo:
	bash evaluation/locomo/run.sh

eval-locomo-full:
	LOCOMO_FULL=1 bash evaluation/locomo/run.sh

clean:
	rm -rf .memdb tmp
