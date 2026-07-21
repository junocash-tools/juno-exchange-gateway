.PHONY: test test-unit test-contract test-release-contract test-docs test-docker test-integration test-e2e test-postgres-smoke compose-config docs-deps docs-build

GO_CACHE := $(CURDIR)/tmp/go-build

test: test-unit test-contract test-docs test-integration test-e2e

test-unit:
	env GOWORK=off GOCACHE=$(GO_CACHE) go test -race ./...
	env GOWORK=off GOCACHE=$(GO_CACHE) go vet ./...

test-contract: compose-config test-release-contract docs-deps
	npm --prefix website run lint:openapi
	git diff --check

test-release-contract:
	./scripts/check-bundle-lock
	./scripts/check-release-tag v1.2.3
	./scripts/check-release-tag v1.2.3-rc.1
	! ./scripts/check-release-tag v01.2.3
	! ./scripts/check-release-tag v1.2
	! ./scripts/check-release-tag v1.2.3-01
	! ./scripts/check-release-tag v1.2.3+build.7
	long_tag="v1.2.3-$$(printf '%123s' '' | tr ' ' a)"; ! ./scripts/check-release-tag "$$long_tag"
	./scripts/check-oci-index testdata/release/oci-index.json linux/amd64 linux/arm64
	! ./scripts/check-oci-index testdata/release/oci-index.json linux/amd64
	./scripts/check-attestations testdata/release/provenance.json testdata/release/sbom.json linux/amd64 linux/arm64
	! ./scripts/check-attestations testdata/release/provenance.json testdata/release/sbom.json linux/amd64
	! ./scripts/check-attestations testdata/release/provenance-missing-materials.json testdata/release/sbom.json linux/amd64 linux/arm64

test-docs: docs-build

test-integration: test-postgres-smoke

test-e2e: test-docker

test-docker:
	./scripts/regtest-e2e

test-postgres-smoke:
	./scripts/postgres-smoke

compose-config:
	./scripts/check-compose

docs-deps:
	npm --prefix website ci --ignore-scripts

docs-build: docs-deps
	npm --prefix website run build
