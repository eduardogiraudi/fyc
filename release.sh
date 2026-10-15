#!/bin/bash
set -euo pipefail

cd "$(dirname "$0")"
source scripts/workflow-common.sh

if [ "$#" -ne 1 ]; then
  workflow_error "Usage: ./release.sh <version> (example: ./release.sh 1.0.0)"
fi
VERSION=$1
TAG="v$VERSION"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

go build -buildvcs=false -o "$work/release-notes" ./cmd/release-notes
notes="$work/release-notes"
"$notes" check-version "$VERSION"
root=$(git rev-parse --show-toplevel)
cd "$root"
[ "$(git symbolic-ref --quiet --short HEAD)" = master ] || workflow_error "Releases must be made from master."
[ -z "$(git status --porcelain)" ] || workflow_error "Commit or stash all changes before releasing."
head=$(git rev-parse HEAD)
if git show-ref --verify --quiet "refs/tags/$TAG"; then
  workflow_error "Tag $TAG already exists. Use a new version."
fi
remote_tag=$(git ls-remote --tags origin "refs/tags/$TAG")
[ -z "$remote_tag" ] || workflow_error "Tag $TAG already exists on origin. Use a new version."

# Use the nearest reachable release tag, not a tag on another branch.
if previous=$(git describe --tags --match 'v[0-9]*' --abbrev=0 "$head" 2>/dev/null); then
  "$notes" check-version "${previous#v}"
  range="$previous..$head"
  echo "Preparing $TAG from commits since $previous..."
else
  range=$head
  echo "No previous release tag found; reviewing all commits for $TAG..."
fi
git log --reverse --no-merges --format='%H%x00%B%x00' "$range" > "$work/commits"
"$notes" draft "$VERSION" "$work/commits" > "$work/section.md"
cp CHANGELOG.md "$work/original-changelog.md"
# Reject duplicate versions before opening the editor or changing release files.
printf '## [%s] - %s\n\n### Changed\n\n- Draft.\n' "$VERSION" "$(date +%F)" > "$work/check.md"
"$notes" assemble "$VERSION" "$work/check.md" "$work/original-changelog.md" > /dev/null

echo 'Only the NEW release section will open in your Git editor.'
echo 'Rewrite or remove TODO entries, and use English user-facing descriptions.'
while true; do
  workflow_edit "$work/section.md"
  if ! "$notes" validate "$VERSION" "$work/section.md"; then
    read -r -p 'Invalid section. Edit again? [y/N] ' answer || answer=n
    case "$answer" in y|Y|yes|YES) continue ;; *) workflow_error "Release cancelled; no release files changed." ;; esac
  fi
  printf '\nRelease notes to publish:\n\n'
  cat "$work/section.md"
  printf '\n'
  read -r -p "Approve these notes and release $TAG? [y/N/e=edit] " answer || answer=n
  case "$answer" in
    y|Y|yes|YES) break ;;
    e|E|edit) continue ;;
    *) echo 'Release cancelled; no release files changed.'; exit 0 ;;
  esac
done

[ "$(git rev-parse HEAD)" = "$head" ] || workflow_error "HEAD changed during review; start again."
[ -z "$(git status --porcelain)" ] || workflow_error "Working tree changed during review; start again."
cmp -s CHANGELOG.md "$work/original-changelog.md" || workflow_error "Changelog changed during review; start again."
if git show-ref --verify --quiet "refs/tags/$TAG"; then
  workflow_error "Tag $TAG appeared during review; start again."
fi
"$notes" assemble "$VERSION" "$work/section.md" CHANGELOG.md > "$work/changelog.md"

echo "Starting approved release process for $TAG..."
go run -buildvcs=false ./cmd/update-codex-version -write
if ! ./update_catalog.sh; then
  echo 'Warning: catalog refresh failed; keeping the existing catalog' >&2
fi
[ "$(git rev-parse HEAD)" = "$head" ] || workflow_error "HEAD changed during refresh; review local changes before retrying."
git diff --cached --quiet || workflow_error "Index changed during refresh; review staged changes before retrying."
[ -z "$(git status --porcelain -- . ':!codexversion/VERSION' ':!models/catalog.json')" ] || workflow_error "Unexpected changes during refresh; review them before retrying."
printf '%s\n' "$VERSION" > version
cat "$work/changelog.md" > CHANGELOG.md
git add -- version CHANGELOG.md codexversion/VERSION models/catalog.json
git commit -m "chore(release): $TAG"
git tag -a "$TAG" -m "Release $VERSION"

# Publish branch and tag together; never trigger CI for an unpublished commit.
echo 'Pushing release commit and tag to origin...'
git push --atomic origin HEAD:refs/heads/master "refs/tags/$TAG"
echo "Release $TAG successfully pushed! GitHub Actions will publish the approved changelog section."
