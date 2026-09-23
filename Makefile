.PHONY: serve migrate test test-chrome lint e2e

serve:
	cd server && go run . serve

migrate:
	cd server && go run . migrate $(ARGS)

test:
	cd server && go build ./... && go vet ./... && go test ./...
	cd client/configwire && dart analyze && dart test

test-chrome:
	cd client/configwire && dart test -p chrome test/chrome_smoke_test.dart

lint:
	cd server && test -z "$$(gofmt -l .)" && go vet ./...
	cd client/configwire && dart analyze
	test -z "$$(grep -rn "dart:ui\|package:flutter" client/configwire/lib || true)"
	test -z "$$(grep -rn "localStorage\|package:web\|dart:js\|PlatformCacheStore\|h[i]ve_ce" client/configwire/lib || true)"
	test -z "$$(grep -rn "import 'dart:io'" client/configwire/lib || true)"

e2e:
	bash scripts/e2e.sh
