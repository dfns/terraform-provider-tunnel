default: fmt lint install generate

build:
	go build -v ./...

install: build
	go install -v ./...

lint:
	golangci-lint run

generate:
	cd tools; go generate ./...

fmt:
	gofmt -s -w -e .

test:
	go test -v -cover -timeout=120s -parallel=10 ./...

test-integration:
	go test -v -tags integration -timeout=120s ./test/integration/

test-e2e:
	go test -v -tags e2e -timeout=10m ./test/e2e/

testacc:
	TF_ACC=1 go test -v -cover -timeout 120m ./...

.PHONY: fmt lint test test-integration test-e2e testacc build install generate
