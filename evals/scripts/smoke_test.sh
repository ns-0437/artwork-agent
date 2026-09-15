#!/usr/bin/env bash
# Manual Day 2 smoke test: runs each seed fixture through the real API and
# prints the resulting findings + artwork_status. Not part of the Day 5 eval
# harness - just a quick end-to-end sanity check during development.
set -euo pipefail

API=http://localhost:8080
FIXDIR="$(dirname "$0")/../fixtures"

run_fixture() {
  local file="$1" width="$2" height="$3" unit="$4" intent="$5" label="$6"

  order_id=$(curl -s -X POST "$API/graphql" -H "Content-Type: application/json" \
    -d "{\"query\":\"mutation(\$input: CreateOrderInput!) { createOrder(input: \$input) { id } }\",\"variables\":{\"input\":{\"ownerId\":\"smoke-test\",\"productType\":\"die-cut-sticker\",\"declaredWidth\":$width,\"declaredHeight\":$height,\"declaredUnit\":\"$unit\",\"intent\":\"$intent\"}}}" \
    | python -c "import sys,json; print(json.load(sys.stdin)['data']['createOrder']['id'])")

  upload_url=$(curl -s -X POST "$API/graphql" -H "Content-Type: application/json" \
    -d "{\"query\":\"mutation(\$orderId: ID!, \$contentType: String!) { createUpload(orderId: \$orderId, contentType: \$contentType) { uploadUrl } }\",\"variables\":{\"orderId\":\"$order_id\",\"contentType\":\"image/png\"}}" \
    | python -c "import sys,json; print(json.load(sys.stdin)['data']['createUpload']['uploadUrl'])")

  curl -s -X POST "$upload_url" -H "Content-Type: image/png" --data-binary @"$FIXDIR/$file" > /dev/null

  curl -s -X POST "$API/graphql" -H "Content-Type: application/json" \
    -d "{\"query\":\"mutation(\$orderId: ID!) { startResolution(orderId: \$orderId) { id } }\",\"variables\":{\"orderId\":\"$order_id\"}}" > /dev/null

  sleep 3

  echo "=== $label ($file, ${width}x${height}$unit, intent=$intent) ==="
  curl -s -X POST "$API/graphql" -H "Content-Type: application/json" \
    -d "{\"query\":\"query(\$id: ID!) { order(id: \$id) { artworkStatus proofStatus findings { checkName result } } }\",\"variables\":{\"id\":\"$order_id\"}}" \
    | python -m json.tool
  echo
}

run_fixture "dev/clean-a_base.png"          3.0 3.0 in "full_bleed" "clean-a (full_bleed)"
run_fixture "held-out/clean-b_base.png"     2.0 2.0 in "border"     "clean-b (border, should resolve)"
run_fixture "dev/lowres-a_base.png"         3.0 3.0 in "border"     "lowres-a (border, low PPI)"
run_fixture "held-out/rgb-noprofile-a_base.png" 2.5 2.5 in "border" "rgb-noprofile-a (border, RGB advisory only, should resolve)"
run_fixture "dev/missing-bleed-a_base.png"  3.0 3.0 in "full_bleed" "missing-bleed-a (full_bleed)"
run_fixture "held-out/border-a_base.png"    4.0 2.0 in "border"     "border-a (border, wide)"
