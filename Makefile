SHELL := /bin/bash
VERSION := $(shell cat VERSION)

.PHONY: agents build build-local test smoke

agents:
	python3 scripts/download-agent-artifacts.py

build:
	./scripts/build-release.sh

build-local:
	mkdir -p artifacts
	cd computer-use && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
		-ldflags "-s -w -X main.version=$(VERSION)" \
		-o ../artifacts/computer-use .

test:
	cd computer-use && test -z "$$(gofmt -l .)" && go vet ./... && go test ./...
	cd computer-use && python3 runtime_test.py
	python3 -m pytest -q tests

smoke:
	python3 scripts/smoke-mcp.py artifacts/computer-use
