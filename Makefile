.PHONY: serve migrate test lint e2e

serve:
	cd server && go run . serve

migrate:
	cd server && go run . migrate $(ARGS)

test:
	cd server && go build ./... && go vet ./... && go test ./...
	cd client/configwire && dart analyze && dart test

lint:
	cd server && test -z "$$(gofmt -l .)" && go vet ./...
	cd client/configwire && dart analyze

e2e:
	bash scripts/e2e.sh
