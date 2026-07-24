.PHONY: build test vet verify

build:
	go build -mod=vendor ./cmd/...

test:
	go test -mod=vendor ./...

vet:
	go vet -mod=vendor ./...

verify: test vet build
	kubectl kustomize deploy/overlays/test >/dev/null
