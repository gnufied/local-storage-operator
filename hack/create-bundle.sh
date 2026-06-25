#!/bin/sh

# A hackish script to build bundle and index images for the given images.
# The output is available in opm-bundle directory.

set -o nounset
set -o pipefail

COVERAGE=false
if [ "${1:-}" = "--coverage" ]; then
    COVERAGE=true
    shift
fi

if [ "$#" -ne "4" ]; then
    echo "Usage: $0 [--coverage] <input_operator_image> <input_diskmaker_image> <output_bundle_image> <output_index_image>"
    exit 1
fi

DEFAULT_TOOL_BIN=$(which podman 2>/dev/null || which docker 2>/dev/null)
if [ "$?" -ne "0" ]; then
	echo "Error: No suitable container manipulation tool (podman, docker) found in \$PATH" 1>&2
	exit 1
fi
TOOL_BIN=${TOOL_BIN:-$DEFAULT_TOOL_BIN}

OPM_BIN=$(which opm 2>/dev/null)
if [ "$?" -ne "0" ]; then
	echo "Error: opm is not found in \$PATH" 1>&2
	exit 1
fi

set -o errexit

TOOL_NAME=$(basename $TOOL_BIN)
SCRIPT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
OPERATOR_IMAGE=$1
DISKMAKER_IMAGE=$2
BUNDLE_IMAGE=$3
INDEX_IMAGE=$4

# Prepare output dir
[ ! -d opm-bundle ] || rm -r opm-bundle # start clean
mkdir -p opm-bundle
pushd opm-bundle
cp -r -v ../config/* .

MANIFEST=manifests/stable/local-storage-operator.clusterserviceversion.yaml

# Replace images in the manifest - error prone, needs to be in sync with image-references.
sed -i.bak -e "s~quay.io/openshift/origin-local-storage-operator:latest~$OPERATOR_IMAGE~" \
	-e "s~quay.io/openshift/origin-local-storage-diskmaker:latest~$DISKMAKER_IMAGE~" \
	$MANIFEST
rm $MANIFEST.bak

if [ "$COVERAGE" = "true" ]; then
    YQ="${SCRIPT_DIR}/bin/yq"
    if [ ! -f "$YQ" ]; then
        echo "Error: yq not found at $YQ. Run 'make ensure-yq' first." 1>&2
        exit 1
    fi

    COVER_DIR="/var/run/coverage"
    DEPLOY='.spec.install.spec.deployments[0].spec.template.spec'
    CONTAINER="${DEPLOY}.containers[0]"

    echo "Injecting coverage configuration into CSV..."
    $YQ -i "${CONTAINER}.env += [{\"name\": \"GOCOVERDIR\", \"value\": \"${COVER_DIR}\"}]" "$MANIFEST"
    $YQ -i "${CONTAINER}.env += [{\"name\": \"LSO_COVERAGE_DIR\", \"value\": \"${COVER_DIR}\"}]" "$MANIFEST"
    $YQ -i "${CONTAINER}.volumeMounts = [{\"name\": \"coverage-data\", \"mountPath\": \"${COVER_DIR}\"}]" "$MANIFEST"
    $YQ -i "${DEPLOY}.volumes = [{\"name\": \"coverage-data\", \"emptyDir\": {}}]" "$MANIFEST"
    $YQ -i "${CONTAINER}.securityContext.readOnlyRootFilesystem = false" "$MANIFEST"
    echo "Coverage configuration injected."
fi

# Build the bundle and push it
$TOOL_BIN build -t $BUNDLE_IMAGE -f bundle.Dockerfile .
$TOOL_BIN push $BUNDLE_IMAGE

# Build the index image and push it
$OPM_BIN index add --bundles $BUNDLE_IMAGE --tag $INDEX_IMAGE --container-tool $TOOL_NAME
$TOOL_BIN push $INDEX_IMAGE


echo
echo --------------------
echo "Index image created"
echo "Copy following snippet to apply it to your cluster"
echo

# Show oc apply -f - <<EOF to copy-paste into shell
cat <<REAL_EOF
oc apply -f - <<EOF
apiVersion: operators.coreos.com/v1alpha1
kind: CatalogSource
metadata:
  name: local-storage
  namespace: openshift-marketplace
spec:
  sourceType: grpc
  image: $INDEX_IMAGE
EOF
REAL_EOF

echo

popd
