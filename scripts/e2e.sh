#!/bin/bash
set -e

# Colors for terminal output
GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m' # No Color

echo "[INFO] Starting integration test suite for Nimbus..."

# 1. Build and Start Servers
echo "[BUILD] Compiling API server and worker binaries..."
go build -o api_server ./cmd/api
go build -o worker_server ./cmd/worker

# The API refuses to start without a strong JWT_SECRET. Generate a throwaway one per run so
# the suite never depends on a checked-in key and never reuses a developer's real key.
export JWT_SECRET="${JWT_SECRET:-$(openssl rand -base64 48)}"

# The suite queries tables directly, so the schema has to exist before the servers start.
echo "[MIGRATE] Applying database migrations..."
make migrate

echo "[INFO] Launching HTTP API service on port 8080..."
./api_server &
API_PID=$!

echo "[INFO] Launching Redis Stream worker process..."
./worker_server &
WORKER_PID=$!

# Ensure graceful termination of background processes upon exit
cleanup() {
    echo "[CLEANUP] Terminating background processes and removing artifacts..."
    kill $API_PID || true
    kill $WORKER_PID || true
    rm -f api_server worker_server dummy_upload.txt downloaded.txt
}
trap cleanup EXIT

# Wait for process initialization
sleep 3

# Check Readiness Probe
echo "[READY] Verifying infrastructure connectivity via deep readiness probe..."
READY_RESP=$(curl -s http://localhost:8080/health/ready)
echo "[DEBUG] Readiness probe response: $READY_RESP"
if echo "$READY_RESP" | grep -q '"status":"OK"'; then
  echo -e "${GREEN}[PASS] Readiness check passed. Database, object store, and message broker are online.${NC}"
else
  echo -e "${RED}[FAIL] Infrastructure dependencies not ready: $READY_RESP${NC}"
  exit 1
fi

# 2. Register User
echo "[TEST] Registering test user account..."
EMAIL="test_$(date +%s)@example.com"
REG_RESP=$(curl -s -X POST http://localhost:8080/api/v1/auth/register \
  -H "Content-Type: application/json" \
  -d "{\"email\":\"$EMAIL\", \"password\":\"secret123\"}")
echo "[DEBUG] Registration response: $REG_RESP"

# 3. Login User and Extract JWT
echo "[TEST] Authenticating to retrieve JWT session token..."
LOGIN_RESP=$(curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d "{\"email\":\"$EMAIL\", \"password\":\"secret123\"}")

set +e
JWT=$(echo $LOGIN_RESP | grep -o '"token":"[^"]*' | grep -o '[^"]*$')
set -e

if [ -z "$JWT" ]; then
  echo -e "${RED}[FAIL] Authentication failed. No token in response.${NC}"
  echo "[DEBUG] Response: $LOGIN_RESP"
  exit 1
fi
echo -e "${GREEN}[PASS] Authentication successful. JWT obtained.${NC}"

# 4. Create a Folder
echo "[TEST] Creating directory container..."
FOLDER_RESP=$(curl -s -X POST http://localhost:8080/api/v1/folders \
  -H "Authorization: Bearer $JWT" \
  -H "Content-Type: application/json" \
  -d "{\"name\":\"My Documents\"}")

FOLDER_ID=$(echo $FOLDER_RESP | grep -o '"id":"[^"]*' | grep -o '[^"]*$')
if [ -z "$FOLDER_ID" ]; then
  echo -e "${RED}[FAIL] Directory creation failed.${NC}"
  echo "[DEBUG] Response: $FOLDER_RESP"
  exit 1
fi
echo -e "${GREEN}[PASS] Directory created. ID: $FOLDER_ID${NC}"

# 5. Upload a File
echo "[TEST] Generating 5MB test file for chunked upload verification..."
# Generate a 5MB text file to verify chunking behavior and provide UTF-8 text for LLM embedding test
yes "Nimbus distributed object storage architecture verification document. Tests chunk deduplication and streaming throughput." | head -c 5242880 > dummy_upload.txt

echo "[TEST] Uploading file with explicit distributed correlation header (X-Request-ID)..."
UPLOAD_RESP=$(curl -s -X POST http://localhost:8080/api/v1/files/upload \
  -H "Authorization: Bearer $JWT" \
  -H "X-Request-ID: test-e2e-trace-9999" \
  -F "file=@dummy_upload.txt" \
  -F "folder_id=$FOLDER_ID")

FILE_ID=$(echo $UPLOAD_RESP | grep -o '"file_id":"[^"]*' | grep -o '[^"]*$')
if [ -z "$FILE_ID" ]; then
  echo -e "${RED}[FAIL] File upload failed.${NC}"
  echo "[DEBUG] Response: $UPLOAD_RESP"
  exit 1
fi
echo -e "${GREEN}[PASS] File upload completed. File ID: $FILE_ID${NC}"

# 6. List Directory
echo "[TEST] Listing directory contents..."
DIR_RESP=$(curl -s -X GET "http://localhost:8080/api/v1/directory?folder_id=$FOLDER_ID" \
  -H "Authorization: Bearer $JWT")
echo "[DEBUG] Directory listing response: $DIR_RESP"

# 7. Download File and Compare
echo "[TEST] Retrieving downloaded stream and verifying integrity..."
curl -s -X GET "http://localhost:8080/api/v1/files/$FILE_ID/download" \
  -H "Authorization: Bearer $JWT" \
  --output downloaded.txt

if cmp -s dummy_upload.txt downloaded.txt; then
    echo -e "${GREEN}[PASS] Downloaded stream matches original file byte-for-byte (zero corruption).${NC}"
else
    echo -e "${RED}[FAIL] Downloaded file integrity check failed (hash mismatch).${NC}"
    ls -l dummy_upload.txt downloaded.txt
    exit 1
fi

# 8. Check Background AI Processing
echo "[TEST] Awaiting background Redis stream consumer to generate semantic tags..."
MAX_RETRIES=15
RETRY_COUNT=0
EMBEDDING_COUNT="0"

while [ "$EMBEDDING_COUNT" != "1" ] && [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
    sleep 3
    EMBEDDING_COUNT=$(docker exec nimbus_db psql -U nimbus -d nimbus_db -t -c "SELECT COUNT(*) FROM file_embeddings WHERE file_id = '$FILE_ID';")
    EMBEDDING_COUNT=$(echo $EMBEDDING_COUNT | xargs)
    RETRY_COUNT=$((RETRY_COUNT+1))
    echo "   [POLLING] Attempt $RETRY_COUNT/$MAX_RETRIES - AI Metadata records found: $EMBEDDING_COUNT"
done

if [ "$EMBEDDING_COUNT" = "1" ]; then
    echo -e "${GREEN}[PASS] Background semantic embeddings generated and persisted to PostgreSQL.${NC}"
else
    echo -e "${RED}[FAIL] Background worker failed to process semantic embeddings within designated timeout.${NC}"
    exit 1
fi

# 9. Test Smart Search
echo "[TEST] Verifying semantic search query matching..."
SEARCH_RESP=$(curl -s -X GET "http://localhost:8080/api/v1/search?q=Nimbus" \
  -H "Authorization: Bearer $JWT")

SEARCH_FILE_ID=$(echo $SEARCH_RESP | grep -o '"id":"[^"]*' | grep -o '[^"]*$' | head -n 1)
if [ "$SEARCH_FILE_ID" = "$FILE_ID" ]; then
  echo -e "${GREEN}[PASS] Semantic query successfully resolved to Target File ID.${NC}"
else
  echo -e "${RED}[FAIL] Semantic search returned unexpected results: $SEARCH_RESP${NC}"
  exit 1
fi

# 10. Test File Deletion
echo "[TEST] Executing file deletion request..."
DELETE_FILE_RESP=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE "http://localhost:8080/api/v1/files/$FILE_ID" -H "Authorization: Bearer $JWT")
if [ "$DELETE_FILE_RESP" = "204" ]; then
  echo -e "${GREEN}[PASS] File cleanly removed from directory metadata.${NC}"
else
  echo -e "${RED}[FAIL] File deletion failed. HTTP Status: $DELETE_FILE_RESP${NC}"
  exit 1
fi

# 11. Test Folder Deletion
echo "[TEST] Executing directory deletion request..."
DELETE_FOLDER_RESP=$(curl -s -o /dev/null -w "%{http_code}" -X DELETE "http://localhost:8080/api/v1/folders/$FOLDER_ID" -H "Authorization: Bearer $JWT")
if [ "$DELETE_FOLDER_RESP" = "204" ]; then
  echo -e "${GREEN}[PASS] Directory cleanly removed from system.${NC}"
else
  echo -e "${RED}[FAIL] Directory deletion failed. HTTP Status: $DELETE_FOLDER_RESP${NC}"
  exit 1
fi

echo -e "${GREEN}[SUCCESS] Integration test suite completed without errors.${NC}"
