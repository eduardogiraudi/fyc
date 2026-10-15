#!/bin/bash
# Refresh the bundled models.dev catalog (models/catalog.json) without cutting a
# release. Uses curl (no Python/Go required for the download itself), stores the
# result atomically, and validates it is JSON when python3 is available.
set -e

cd "$(dirname "$0")"

CATALOG_URL="${CATALOG_URL:-https://models.dev/api.json}"
CATALOG_FILE="models/catalog.json"

if ! command -v curl >/dev/null 2>&1; then
  echo "error: curl is required" >&2
  exit 1
fi

echo "Fetching $CATALOG_URL -> $CATALOG_FILE ..."
tmp="$(mktemp "${CATALOG_FILE}.XXXXXX")"
trap 'rm -f "$tmp"' EXIT

if ! curl -fsSL "$CATALOG_URL" -o "$tmp"; then
  echo "error: download failed (check your network)" >&2
  exit 1
fi
if [ ! -s "$tmp" ]; then
  echo "error: downloaded catalog is empty" >&2
  exit 1
fi
if command -v python3 >/dev/null 2>&1; then
  if ! python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$tmp"; then
    echo "error: downloaded catalog is not valid JSON" >&2
    exit 1
  fi
fi

mv "$tmp" "$CATALOG_FILE"
trap - EXIT
echo "Updated $CATALOG_FILE ($(wc -c < "$CATALOG_FILE") bytes)."
echo "Ship it with the next release (release.sh includes models/ in the archive);"
echo "for the installed copy run 'fuckyourcli models update'."
