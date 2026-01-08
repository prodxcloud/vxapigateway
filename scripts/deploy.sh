#!/bin/bash

# Build and Deploy Script

set -e

echo "🚀 VA API Gateway - Build & Deploy"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# Parse arguments
ENVIRONMENT="${1:-development}"
BUILD_TYPE="${2:-docker}"

echo "Environment: $ENVIRONMENT"
echo "Build Type: $BUILD_TYPE"
echo ""

# Load environment variables
if [ -f ".env.${ENVIRONMENT}" ]; then
    echo "Loading environment from .env.${ENVIRONMENT}"
    export $(cat ".env.${ENVIRONMENT}" | xargs)
fi

if [ "$BUILD_TYPE" == "docker" ]; then
    echo "🐳 Building Docker image..."
    docker build -t va-api-gateway:latest .
    
    echo "✅ Docker image built successfully"
    echo ""
    echo "Starting services with docker-compose..."
    docker-compose up -d
    
    echo ""
    echo "Waiting for services to be healthy..."
    sleep 10
    
    echo ""
    echo "Service Status:"
    docker-compose ps
    
    echo ""
    echo "Gateway Health:"
    curl -s http://localhost:8080/health | jq .
    
elif [ "$BUILD_TYPE" == "kubernetes" ]; then
    echo "☸️  Deploying to Kubernetes..."
    
    # Build and push image
    echo "Building image..."
    docker build -t va-api-gateway:${ENVIRONMENT} .
    
    # Tag and push (adjust for your registry)
    REGISTRY="${DOCKER_REGISTRY:-localhost:5000}"
    docker tag va-api-gateway:${ENVIRONMENT} ${REGISTRY}/va-api-gateway:${ENVIRONMENT}
    docker push ${REGISTRY}/va-api-gateway:${ENVIRONMENT}
    
    # Apply Kubernetes manifests
    echo "Applying Kubernetes manifests..."
    kubectl apply -f k8s/deployment.yaml
    kubectl apply -f k8s/redis.yaml
    kubectl apply -f k8s/ingress.yaml
    
    echo ""
    echo "Waiting for rollout..."
    kubectl rollout status deployment/api-gateway -n va-gateway
    
    echo ""
    echo "Service Status:"
    kubectl get pods -n va-gateway
    kubectl get services -n va-gateway
    
elif [ "$BUILD_TYPE" == "local" ]; then
    echo "🏠 Building for local development..."
    
    # Download dependencies
    echo "Downloading dependencies..."
    go mod download
    go mod tidy
    
    # Run tests
    echo "Running tests..."
    go test -v ./...
    
    # Build binary
    echo "Building binary..."
    go build -o gateway .
    
    echo "✅ Build complete: ./gateway"
    echo ""
    echo "To run locally:"
    echo "  ./gateway"
    
else
    echo "❌ Unknown build type: $BUILD_TYPE"
    echo "Usage: $0 [environment] [docker|kubernetes|local]"
    exit 1
fi

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "✅ Deployment completed!"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "📌 Quick Links:"
echo "  Gateway:    http://localhost:8080"
echo "  Health:     http://localhost:8080/health"
echo "  Metrics:    http://localhost:8080/metrics"
echo "  Prometheus: http://localhost:9090"
echo "  Grafana:    http://localhost:3000 (admin/admin)"
echo "  Jaeger:     http://localhost:16686"
echo ""
echo "📚 Next Steps:"
echo "  1. Run load tests: ./scripts/load-test.sh"
echo "  2. View metrics in Grafana"
echo "  3. Check distributed traces in Jaeger"
echo "  4. Configure your backend services"
