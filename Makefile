# Makefile for VA API Gateway

.PHONY: help build run test clean docker-build docker-up docker-down k8s-deploy k8s-delete load-test

# Variables
APP_NAME=gateway
DOCKER_IMAGE=va-api-gateway
VERSION?=latest
NAMESPACE=va-gateway

help: ## Show this help message
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Available targets:'
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the Go binary
	@echo "Building $(APP_NAME)..."
	go build -o $(APP_NAME) .
	@echo "Build complete: ./$(APP_NAME)"

run: ## Run the application locally
	@echo "Starting $(APP_NAME)..."
	go run main.go

test: ## Run tests
	@echo "Running tests..."
	go test -v -cover ./...

test-race: ## Run tests with race detector
	@echo "Running tests with race detector..."
	go test -v -race ./...

clean: ## Clean build artifacts
	@echo "Cleaning..."
	rm -f $(APP_NAME)
	rm -rf dist/
	go clean

deps: ## Download dependencies
	@echo "Downloading dependencies..."
	go mod download
	go mod tidy

fmt: ## Format code
	@echo "Formatting code..."
	go fmt ./...
	gofmt -s -w .

lint: ## Run linter
	@echo "Running linter..."
	golangci-lint run

# Docker targets
docker-build: ## Build Docker image
	@echo "Building Docker image..."
	docker build -t $(DOCKER_IMAGE):$(VERSION) .

docker-up: ## Start services with docker-compose
	@echo "Starting services..."
	docker-compose up -d
	@echo "Services started. Gateway: http://localhost:8080"

docker-down: ## Stop services
	@echo "Stopping services..."
	docker-compose down

docker-logs: ## View docker logs
	docker-compose logs -f gateway

docker-ps: ## List running containers
	docker-compose ps

# Kubernetes targets
k8s-deploy: ## Deploy to Kubernetes
	@echo "Deploying to Kubernetes..."
	kubectl apply -f k8s/deployment.yaml
	kubectl apply -f k8s/redis.yaml
	kubectl apply -f k8s/ingress.yaml
	@echo "Waiting for rollout..."
	kubectl rollout status deployment/api-gateway -n $(NAMESPACE)

k8s-delete: ## Delete from Kubernetes
	@echo "Deleting from Kubernetes..."
	kubectl delete -f k8s/deployment.yaml
	kubectl delete -f k8s/redis.yaml
	kubectl delete -f k8s/ingress.yaml

k8s-status: ## Check Kubernetes status
	@echo "Pods:"
	kubectl get pods -n $(NAMESPACE)
	@echo ""
	@echo "Services:"
	kubectl get services -n $(NAMESPACE)
	@echo ""
	@echo "Ingress:"
	kubectl get ingress -n $(NAMESPACE)

k8s-logs: ## View Kubernetes logs
	kubectl logs -f deployment/api-gateway -n $(NAMESPACE)

# Testing targets
load-test: ## Run load tests
	@echo "Running load tests..."
	./scripts/load-test.sh

health: ## Check gateway health
	@curl -s http://localhost:8080/health | jq .

metrics: ## View metrics
	@curl -s http://localhost:8080/metrics | grep -E "http_requests_total|active_connections"

# Development helpers
dev: ## Start in development mode with auto-reload
	@echo "Starting in development mode..."
	air

install-tools: ## Install development tools
	@echo "Installing tools..."
	go install github.com/cosmtrek/air@latest
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

# Release targets
release: clean test build ## Create a release build
	@echo "Creating release..."
	mkdir -p dist
	cp $(APP_NAME) dist/
	cp README.md dist/
	cp LICENSE dist/
	cd dist && tar -czf $(APP_NAME)-$(VERSION).tar.gz *
	@echo "Release created: dist/$(APP_NAME)-$(VERSION).tar.gz"

.DEFAULT_GOAL := help
