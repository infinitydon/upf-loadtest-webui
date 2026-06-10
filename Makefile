IMAGE ?= ghcr.io/infinitydon/upf-loadtest-webui
TAG ?= v0.1.25

.PHONY: build test image
build:
	cd frontend && npm install && npm run build
	go build ./...

test:
	go test ./...
	helm lint chart

image:
	docker build -t $(IMAGE):$(TAG) .
