#!/usr/bin/env bash
# Host-side driver. Builds the demo image, runs it, and drops
# leo-demo.cast + leo-demo.gif next to this script.
#
# Usage:
#   bash docs/demo/record.sh                  # build + run
#   SKIP_BUILD=1 bash docs/demo/record.sh     # reuse existing image
#
# Requires: docker (daemon running).

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE/../.." && pwd)"
OUT_DIR="$HERE/out"
IMAGE="${IMAGE:-leo-demo:latest}"

mkdir -p "$OUT_DIR"
rm -f "$OUT_DIR/leo-demo.cast" "$OUT_DIR/leo-demo.gif"

if [[ "${SKIP_BUILD:-0}" != "1" ]]; then
  echo "[record] building image $IMAGE ..."
  docker build \
    --file "$HERE/Dockerfile" \
    --tag "$IMAGE" \
    "$REPO_ROOT"
fi

echo "[record] running demo container ..."
docker run --rm \
  --name leo-demo-run \
  -v "$OUT_DIR:/home/demo/out" \
  "$IMAGE"

echo "[record] artifacts:"
ls -lh "$OUT_DIR"
