CORPUS ?= cdc-pipeline
ROOT    ?= ../cdc-pipeline
K       ?= 5

.PHONY: help
help:
	@echo "make up          start postgres+pgvector on :5433"
	@echo "make down        stop it"
	@echo "make build       build ./bin/rag"
	@echo "make test        run unit tests"
	@echo "make index       index \$$ROOT as corpus \$$CORPUS"
	@echo "make stats       show what is indexed"
	@echo "make eval        score retrieval against the golden set"
	@echo "make sweep       compare all four retrieval modes"
	@echo "make baseline    regenerate eval/baseline/corpus.txt"
	@echo "make smoke       one retrieval-only query, no API key needed"
	@echo ""
	@echo "ask a question:  ./bin/rag ask -corpus $(CORPUS) -show-sources \"your question\""

.PHONY: up
up:
	docker compose up -d
	@echo "waiting for postgres..."
	@until docker compose exec -T postgres pg_isready -U rag -d rag >/dev/null 2>&1; do sleep 1; done
	@echo "ready on localhost:5433"

.PHONY: down
down:
	docker compose down

.PHONY: build
build:
	go build -o bin/rag ./cmd/rag

.PHONY: test
test:
	go test ./...

.PHONY: index
index: build
	./bin/rag index -corpus $(CORPUS) -root $(ROOT)

.PHONY: eval
eval: build
	./bin/rag eval -corpus $(CORPUS) -k 10 -mode hybrid+mmr -candidates 40

.PHONY: sweep
sweep: build
	./bin/rag eval -corpus $(CORPUS) -k 10 -sweep -candidates 40

.PHONY: stats
stats: build
	./bin/rag stats -corpus $(CORPUS)

.PHONY: baseline
baseline:
	./scripts/pack-corpus.sh $(ROOT) eval/baseline/corpus.txt

# Retrieval only - exercises Ollama and pgvector without spending API tokens.
# Run this first after setup; if it works, only generation can still be broken.
.PHONY: smoke
smoke: build
	./bin/rag ask -corpus $(CORPUS) -retrieve-only -k $(K) \
		"how does the consumer handle a message it cannot process?"
