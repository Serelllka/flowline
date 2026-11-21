.PHONY: run build test check

run:
	cd backend && go run ./cmd/service

build:
	cd backend && go build -o ../bin/flowline ./cmd/service

test:
	cd backend && go test -race ./...
	node --test frontend/*.test.cjs

check:
	cd backend && go vet ./...
	@for file in frontend/*.js; do node --check "$$file" || exit; done
