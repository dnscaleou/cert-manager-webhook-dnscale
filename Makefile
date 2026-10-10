GO ?= go
export GOCACHE := $(CURDIR)/.gocache

.PHONY: test test-conformance test-live-conformance

test:
	$(GO) mod verify
	$(GO) test -race -count=1 ./...
	$(GO) vet ./...

test-conformance:
	envtest_assets="$${KUBEBUILDER_ASSETS:-$$(python3 scripts/setup-envtest.py)}" && \
		TEST_ASSET_ETCD="$$envtest_assets/etcd" TEST_ASSET_KUBE_APISERVER="$$envtest_assets/kube-apiserver" \
		TEST_ASSET_KUBECTL="$$envtest_assets/kubectl" \
		$(GO) test -race -tags=conformance -run '^TestDNS01Conformance$$' -count=1 -timeout=5m -v .

test-live-conformance:
	@test "$(DNSCALE_LIVE_TESTS)" = true || (echo 'Set DNSCALE_LIVE_TESTS=true to authorize live TXT writes'; exit 1)
	@test -n "$(DNSCALE_TEST_ZONE)" && test -n "$(DNSCALE_TEST_TOKEN_FILE)"
	envtest_assets="$${KUBEBUILDER_ASSETS:-$$(python3 scripts/setup-envtest.py)}" && \
		TEST_ASSET_ETCD="$$envtest_assets/etcd" TEST_ASSET_KUBE_APISERVER="$$envtest_assets/kube-apiserver" \
		TEST_ASSET_KUBECTL="$$envtest_assets/kubectl" \
		$(GO) test -race -tags=conformance -run '^TestDNS01LiveConformance$$' -count=1 -timeout=30m -v .
