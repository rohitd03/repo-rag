CORPUS ?= cdc-pipeline
ROOT    ?= ../cdc-pipeline
K       ?= 5

.PHONY: help
help:
	@echo "make up          terraform apply: postgres+pgvector on :5433"
	@echo "make plan        terraform plan for the local infra"
	@echo "make down        terraform destroy"
	@echo "make build       build ./bin/rag"
	@echo "make test        run unit tests"
	@echo "make index       index \$$ROOT as corpus \$$CORPUS"
	@echo "make stats       show what is indexed"
	@echo "make eval        score retrieval against the golden set"
	@echo "make sweep       compare all four retrieval modes"
	@echo "make answer-eval score generated answers end to end"
	@echo "make baseline    regenerate eval/baseline/corpus.txt"
	@echo "make smoke       one retrieval-only query, no API key needed"
	@echo "make tf-check    fmt + validate both terraform modules"
	@echo ""
	@echo "ask a question:  ./bin/rag ask -corpus $(CORPUS) -show-sources \"your question\""

TF := terraform -chdir=terraform/local

.PHONY: up
up:
	$(TF) init -input=false
	$(TF) apply -auto-approve
	@echo "postgres ready on localhost:$$($(TF) output -raw host_port)"

.PHONY: plan
plan:
	$(TF) init -input=false
	$(TF) plan

.PHONY: down
down:
	$(TF) destroy -auto-approve

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

.PHONY: answer-eval
answer-eval: build
	./bin/rag answer-eval -corpus $(CORPUS) -k 10 -mode hybrid+mmr -candidates 40

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

.PHONY: tf-check
tf-check:
	terraform -chdir=terraform/local fmt -check -diff
	terraform -chdir=terraform/local validate
	terraform -chdir=terraform/aws fmt -check -diff
	terraform -chdir=terraform/aws validate
