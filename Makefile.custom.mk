# Custom targets — extends the devctl-generated Makefile.gen.*.mk (which already
# provides build, test, lint, fmt, vet, install, run).

NAME ?= mcp-timescale
CHART_DIR ?= ./helm/$(NAME)

##@ Develop

.PHONY: run-stdio
run-stdio: ## Run on stdio (local dev / Claude Desktop); needs MCP_TIMESCALE_DSN or a databases file
	go run . serve --transport=stdio

.PHONY: run-http
run-http: ## Run on streamable-HTTP without OAuth (local dev only; every call runs as caller "local")
	OAUTH_ENABLED=false go run . serve --transport=streamable-http

.PHONY: test-integration
test-integration: ## Run the integration suite against a throwaway TimescaleDB container (Docker) or MCP_TIMESCALE_TEST_DSN
	./scripts/integration-test.sh

##@ Helm

.PHONY: helm-lint
helm-lint: ## helm lint the chart
	helm lint $(CHART_DIR)
	helm lint $(CHART_DIR) -f $(CHART_DIR)/ci/ci-values.yaml

.PHONY: helm-template
helm-template: ## Render chart with the CI values
	helm template $(NAME) $(CHART_DIR) -f $(CHART_DIR)/ci/ci-values.yaml

##@ Tidy

.PHONY: tidy
tidy: ## go mod tidy
	go mod tidy
