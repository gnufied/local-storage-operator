#!/bin/bash
set -euo pipefail

NAMESPACE=${TEST_OPERATOR_NAMESPACE:-openshift-local-storage}
OUTPUT_DIR=${COVER_OUTPUT_DIR:-_output/coverage}
COVER_DIR="/var/run/coverage"
SKIP_TESTS=${SKIP_TESTS:-false}
SUITE_ARGS=()
E2E_ARGS=()

usage() {
  cat <<'EOF'
Usage: hack/e2e-coverage.sh [options] [-- go test args...]

Runs e2e tests against a coverage-instrumented LSO deployment, then collects
and merges coverage data from all pods (operator + DaemonSets).

The operator must already be deployed with coverage support baked in.
Use 'make bundle-cover' to build and deploy a coverage-enabled bundle.

Options:
  --skip-tests          Skip running e2e tests (just collect coverage)
  --suite <name>        Run specific e2e suite (passed to test-e2e.sh)
  --output <dir>        Output directory (default: _output/coverage)
  -h, --help            Show this help

Environment variables:
  TEST_OPERATOR_NAMESPACE  Namespace (default: openshift-local-storage)
  COVER_OUTPUT_DIR      Same as --output

Example:
  # Build and deploy coverage bundle first:
  make bundle-cover REGISTRY=quay.io/myuser VERSION=cover-1

  # Then run e2e tests and collect coverage:
  hack/e2e-coverage.sh --suite LocalVolumeSet
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --skip-tests)   SKIP_TESTS=true; shift ;;
    --suite)        SUITE_ARGS=(--suite "$2"); shift 2 ;;
    --suite=*)      SUITE_ARGS=(--suite "${1#*=}"); shift ;;
    --output)       OUTPUT_DIR="$2"; shift 2 ;;
    --output=*)     OUTPUT_DIR="${1#*=}"; shift ;;
    -h|--help)      usage; exit 0 ;;
    --)             shift; E2E_ARGS=("$@"); break ;;
    *)              E2E_ARGS+=("$1"); shift ;;
  esac
done

rm -rf "$OUTPUT_DIR"
mkdir -p "$OUTPUT_DIR"

# --- Verify coverage is configured ---
echo "=== Verifying coverage configuration ==="
OPERATOR_POD=$(oc get pods -n "$NAMESPACE" -l name=local-storage-operator -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
if [[ -z "$OPERATOR_POD" ]]; then
  echo "error: no operator pod found in namespace $NAMESPACE" >&2
  echo "Deploy LSO with 'make bundle-cover' first." >&2
  exit 1
fi

HAS_COVERDIR=$(oc exec -n "$NAMESPACE" "$OPERATOR_POD" -- printenv GOCOVERDIR 2>/dev/null || true)
if [[ -z "$HAS_COVERDIR" ]]; then
  echo "error: operator pod does not have GOCOVERDIR set" >&2
  echo "Deploy LSO with 'make bundle-cover' to enable coverage." >&2
  exit 1
fi
echo "Operator pod $OPERATOR_POD has GOCOVERDIR=$HAS_COVERDIR"

# --- Run e2e tests ---
if [[ "$SKIP_TESTS" != "true" ]]; then
  echo "=== Running e2e tests ==="
  hack/test-e2e.sh "${SUITE_ARGS[@]+"${SUITE_ARGS[@]}"}" "${E2E_ARGS[@]+"${E2E_ARGS[@]}"}" || true
fi

# --- Collect coverage data ---
echo "=== Collecting coverage data ==="

# Diskmaker pod coverage is collected by the test code itself (via SIGUSR1)
# before CR deletion. Here we only collect from the operator pod, which
# persists throughout all tests.

echo "  Signaling operator pod for coverage flush..."
oc exec -n "$NAMESPACE" "$OPERATOR_POD" -- kill -USR1 1 2>/dev/null || true
sleep 3
mkdir -p "$OUTPUT_DIR/operator"
echo "  Copying coverage data from operator..."
oc cp "$NAMESPACE/$OPERATOR_POD:$COVER_DIR" "$OUTPUT_DIR/operator" 2>/dev/null || true

# --- Merge and report ---
echo "=== Merging coverage data ==="

MERGE_DIRS=""
for dir in "$OUTPUT_DIR"/*/; do
  if ls "$dir"/*.* >/dev/null 2>&1; then
    MERGE_DIRS="${MERGE_DIRS:+$MERGE_DIRS,}$dir"
  fi
done

if [[ -z "$MERGE_DIRS" ]]; then
  echo "No coverage data collected. Check that:"
  echo "  - LSO was deployed with 'make bundle-cover'"
  echo "  - GOCOVERDIR was set in the pods"
  echo "  - Pods exited gracefully (SIGTERM, not SIGKILL)"
  exit 1
fi

mkdir -p "$OUTPUT_DIR/merged"
go tool covdata merge -i="$MERGE_DIRS" -o "$OUTPUT_DIR/merged"

echo "=== Generating reports ==="
go tool covdata textfmt -i="$OUTPUT_DIR/merged" -o "$OUTPUT_DIR/coverage.txt"
go tool cover -func="$OUTPUT_DIR/coverage.txt" | tail -1
go tool cover -html="$OUTPUT_DIR/coverage.txt" -o "$OUTPUT_DIR/coverage.html"

echo ""
echo "Coverage reports:"
echo "  Text profile: $OUTPUT_DIR/coverage.txt"
echo "  HTML report:  $OUTPUT_DIR/coverage.html"
echo "  Function summary: go tool cover -func=$OUTPUT_DIR/coverage.txt"
