# Custom targets — extends the devctl-generated Makefile.gen.*.mk (which already
# provides build, test, lint, fmt, vet, install, run and the release packaging).

NAME ?= mcp-timescale
CHART_DIR ?= ./helm/$(NAME)

##@ Develop

.PHONY: run-stdio
run-stdio: ## Run on stdio (local dev / Claude Desktop); needs MCP_TIMESCALE_DSN or a databases file
	go run . serve --transport=stdio

.PHONY: run-http
run-http: ## Run on streamable-HTTP without OAuth (local dev only; every call runs as caller "local")
	OAUTH_ENABLED=false go run . serve --transport=streamable-http

##@ Testing

.PHONY: test-vet
test-vet: ## Run go test with coverage and go vet
	NO_COLOR=true go test -cover ./...
	go vet ./...

.PHONY: test-integration
test-integration: ## Run the integration suite against a throwaway TimescaleDB container (Docker) or MCP_TIMESCALE_TEST_DSN
	./scripts/integration-test.sh

.PHONY: govulncheck
govulncheck: ## Scan the module graph for known vulnerabilities
	@command -v govulncheck >/dev/null 2>&1 || { echo "Installing govulncheck..."; go install golang.org/x/vuln/cmd/govulncheck@latest; }
	govulncheck ./...

##@ Helm

.PHONY: helm-lint
helm-lint: ## helm lint the chart with its defaults and with every ci/*-values.yaml case
	helm lint $(CHART_DIR)
	@set -e; for f in $(CHART_DIR)/ci/*-values.yaml; do echo "====> helm lint $(CHART_DIR) -f $$f"; helm lint $(CHART_DIR) -f $$f; done

.PHONY: helm-template
helm-template: ## Render chart with the CI values
	helm template $(NAME) $(CHART_DIR) -f $(CHART_DIR)/ci/ci-values.yaml

.PHONY: helm-test
helm-test: ## Run the chart unit tests in helm/$(NAME)/tests (requires the helm unittest plugin)
	helm unittest $(CHART_DIR)

.PHONY: helm-verify-labels
helm-verify-labels: ## Package the chart with branch-build versions whose 63-character cut lands on a dot and assert every helm.sh/chart and app.kubernetes.io/name label and metadata.name stays valid (#7)
	CHART_DIR=$(CHART_DIR) ./scripts/verify-chart-labels.sh

##@ Tidy

.PHONY: tidy
tidy: ## go mod tidy
	go mod tidy
