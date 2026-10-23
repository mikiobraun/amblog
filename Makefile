# Common tasks. The local test network is scripts/dev.sh; see its header.

.PHONY: build test check fixtures front dev dev-start dev-air dev-stop dev-reset

build:
	go build -o . ./cmd/...

test:
	go test ./...
	cd amfront && npm test

check:
	gofmt -l . | (! grep .)
	go vet ./...
	go test ./...
	cd amfront && npm run typecheck && npm test

# Regenerate amfront's signing fixtures from a real amfeed.
fixtures:
	amfront/scripts/gen-fixtures.sh

# amfront dev server on :8300. Extra hostnames: AMFRONT_ALLOWED_HOSTS (env or amfront/.env.local).
front:
	cd amfront && npm run dev

# Local network: alice, bob, carol (amfeed :9100-9102) and an aggregator (:9200).
# Public URLs behind a reverse proxy: make dev AMDEV_URL='https://{name}.dev.example'
dev:
	scripts/dev.sh setup

dev-start:
	scripts/dev.sh start

# Rebuild and restart the dev network on every Go change (air: go install github.com/air-verse/air@latest).
dev-air:
	air

dev-stop:
	scripts/dev.sh stop

dev-reset:
	scripts/dev.sh reset
