.PHONY: build vet deploy down sub client test bootstrap doctor release fmt e2e

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

build:
	go build -ldflags "$(LDFLAGS)" ./...

vet:
	go vet ./...

deploy:
	docker compose up -d --build

down:
	docker compose down

sub:
	./scripts/make-sub.sh

client:
	./scripts/client-setup.sh $(if $(DSN),--dsn "$(DSN)",)

test:
	go test ./...

e2e:
	./scripts/e2e.sh

bootstrap:
	./scripts/bootstrap.sh

doctor:
	./scripts/doctor.sh

release:
	mkdir -p dist
	for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64; do \
		os=$${target%/*}; arch=$${target#*/}; ext=""; \
		[ "$$os" = "windows" ] && ext=".exe"; \
		echo "building dist/pgtun-$$os-$$arch$$ext"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o "dist/pgtun-$$os-$$arch$$ext" ./cmd/pgtun || exit 1; \
	done

fmt:
	gofmt -w .
