# Custom targets — extends the devctl-generated Makefile.gen.*.mk.

NAME ?= mcp-template
CHART_DIR ?= ./helm/$(NAME)

##@ Develop

.PHONY: lint
lint: ## Run golangci-lint
	golangci-lint run ./...

.PHONY: vet
vet: ## go vet
	go vet ./...

.PHONY: test
test: ## Run unit tests
	go test ./...

.PHONY: build
build: ## Build the binary
	CGO_ENABLED=0 go build -o bin/$(NAME) .

.PHONY: run
run: ## Run on stdio (local dev / Claude Desktop)
	go run . serve --transport=stdio

.PHONY: run-http
run-http: ## Run on streamable-HTTP without OAuth (local dev only)
	OAUTH_ENABLED=false go run . serve --transport=streamable-http

##@ Helm

.PHONY: helm-lint
helm-lint: ## helm lint the chart
	helm lint $(CHART_DIR)

.PHONY: helm-template
helm-template: ## Render chart with default values
	helm template $(NAME) $(CHART_DIR)

##@ Tidy

.PHONY: tidy
tidy: ## go mod tidy
	go mod tidy

.PHONY: fmt
fmt: ## gofmt
	gofmt -s -w .
