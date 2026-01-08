#!/bin/bash

# Load Testing Script for VA API Gateway
# Requires: Apache Bench (ab) or wrk

set -e

GATEWAY_URL="${GATEWAY_URL:-http://localhost:8080}"
DURATION="${DURATION:-60}"
CONNECTIONS="${CONNECTIONS:-100}"
THREADS="${THREADS:-10}"

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "🔥 VA API Gateway Load Test"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "Target: $GATEWAY_URL"
echo "Duration: ${DURATION}s"
echo "Connections: $CONNECTIONS"
echo "Threads: $THREADS"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# Check if wrk is available
if command -v wrk &> /dev/null; then
    echo "Using wrk for load testing..."
    
    # Test 1: Health endpoint
    echo ""
    echo "📊 Test 1: Health Endpoint"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    wrk -t${THREADS} -c${CONNECTIONS} -d${DURATION}s "${GATEWAY_URL}/health"
    
    # Test 2: Authenticated endpoint with JWT
    echo ""
    echo "📊 Test 2: Authenticated Endpoint (with JWT)"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    JWT_TOKEN="eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ0ZXN0LXVzZXIiLCJleHAiOjk5OTk5OTk5OTl9.test"
    wrk -t${THREADS} -c${CONNECTIONS} -d${DURATION}s \
        -H "Authorization: Bearer ${JWT_TOKEN}" \
        "${GATEWAY_URL}/api/test"
    
    # Test 3: API Key authentication
    echo ""
    echo "📊 Test 3: API Key Authentication"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    wrk -t${THREADS} -c${CONNECTIONS} -d${DURATION}s \
        -H "X-API-Key: demo-api-key-123" \
        "${GATEWAY_URL}/api/test"
    
    # Test 4: DDoS simulation (high rate)
    echo ""
    echo "📊 Test 4: DDoS Protection Test (High Rate)"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    wrk -t50 -c500 -d10s "${GATEWAY_URL}/health"

elif command -v ab &> /dev/null; then
    echo "Using Apache Bench (ab) for load testing..."
    
    # Test 1: Health endpoint
    echo ""
    echo "📊 Test 1: Health Endpoint"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    ab -n 10000 -c ${CONNECTIONS} "${GATEWAY_URL}/health"
    
    # Test 2: With authentication
    echo ""
    echo "📊 Test 2: Authenticated Endpoint"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    ab -n 10000 -c ${CONNECTIONS} \
        -H "X-API-Key: demo-api-key-123" \
        "${GATEWAY_URL}/api/test"

else
    echo "❌ Neither wrk nor ab found. Please install one:"
    echo "   - wrk: https://github.com/wg/wrk"
    echo "   - ab: sudo apt-get install apache2-utils"
    exit 1
fi

# Fetch metrics
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "📈 Fetching Gateway Metrics..."
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
curl -s "${GATEWAY_URL}/metrics" | grep -E "http_requests_total|http_request_duration|active_connections|blocked_ips|circuit_breaker"

echo ""
echo "✅ Load test completed!"
echo "View detailed metrics at: ${GATEWAY_URL}/metrics"
echo "View Prometheus at: http://localhost:9090"
echo "View Grafana at: http://localhost:3000"
