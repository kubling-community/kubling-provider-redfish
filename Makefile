GO ?= go
BINARY ?= bin/kubling-provider-redfish
IMAGE ?= kubling-provider-redfish:local
PACKAGE := ./cmd/kubling-provider-redfish
SAMPLE_COMPOSE := docker compose --file examples/kubling-latest/compose.yaml

.PHONY: build build-check check container-build container-e2e fmt fmt-check mod-check run sample sample-down sample-test sample-up schema schema-check semantic semantic-check test test-race vet

build:
	mkdir -p $(dir $(BINARY))
	$(GO) build -trimpath -o $(BINARY) $(PACKAGE)

build-check:
	$(GO) build ./...

container-build:
	docker build --tag $(IMAGE) .

container-e2e:
	bash test/container/run.sh

run:
	$(GO) run $(PACKAGE)

sample:
	bash examples/kubling-latest/scripts/run.sh

sample-up:
	$(SAMPLE_COMPOSE) up --build --pull always --wait --wait-timeout 180

sample-test:
	$(SAMPLE_COMPOSE) --profile test run --rm --no-deps smoke-test

sample-down:
	$(SAMPLE_COMPOSE) down --volumes --remove-orphans --rmi local

schema:
	$(GO) run ./cmd/redfish-schema-gen

schema-check:
	$(GO) run ./cmd/redfish-schema-gen --check

semantic:
	$(GO) run ./cmd/redfish-semantic-gen

semantic-check:
	$(GO) run ./cmd/redfish-semantic-gen --check

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

fmt-check:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

mod-check:
	$(GO) mod tidy -diff

check: mod-check fmt-check schema-check semantic-check vet test-race build-check
