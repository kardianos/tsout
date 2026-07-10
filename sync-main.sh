#!/usr/bin/env bash
# Rebuild kardianos/main as: microsoft/typescript-go main + outfile.patch
#
# Branches:
#   main          — published tree (upstream HEAD + one commit). Force-pushed with lease.
#   outfile-patch — durable patchset only (outfile.patch). Normal push, never force.
#
# Remotes:
#   origin     — https://github.com/microsoft/typescript-go
#   kardianos  — https://github.com/kardianos/tsout
set -euo pipefail

REMOTE_UPSTREAM="${REMOTE_UPSTREAM:-origin}"
REMOTE_PUBLISH="${REMOTE_PUBLISH:-kardianos}"
UPSTREAM_BRANCH="${UPSTREAM_BRANCH:-main}"
PUBLISH_BRANCH="${PUBLISH_BRANCH:-main}"
PATCH_BRANCH="${PATCH_BRANCH:-outfile-patch}"
PATCH_FILE="${PATCH_FILE:-outfile.patch}"
COMMIT_MSG="${COMMIT_MSG:-internal/compiler: support outfile and cross file namespaces}"

die() { echo "error: $*" >&2; exit 1; }

require_clean() {
  if [[ -n "$(git status --porcelain)" ]]; then
    die "working tree is dirty; commit or stash first"
  fi
}

require_remote() {
  git remote get-url "$1" >/dev/null 2>&1 || die "missing git remote: $1"
}

# Resolve patch blob: local branch, then publish remote.
resolve_patch_ref() {
  if git rev-parse --verify -q "refs/heads/${PATCH_BRANCH}" >/dev/null; then
    echo "refs/heads/${PATCH_BRANCH}"
    return
  fi
  if git rev-parse --verify -q "refs/remotes/${REMOTE_PUBLISH}/${PATCH_BRANCH}" >/dev/null; then
    echo "refs/remotes/${REMOTE_PUBLISH}/${PATCH_BRANCH}"
    return
  fi
  die "cannot find ${PATCH_BRANCH} (local or ${REMOTE_PUBLISH}/${PATCH_BRANCH}); create it first"
}

echo ">>> Fetching ${REMOTE_UPSTREAM}/${UPSTREAM_BRANCH} and ${REMOTE_PUBLISH}"
require_remote "$REMOTE_UPSTREAM"
require_remote "$REMOTE_PUBLISH"
require_clean
git fetch "$REMOTE_UPSTREAM" "$UPSTREAM_BRANCH"
git fetch "$REMOTE_PUBLISH" "+refs/heads/${PATCH_BRANCH}:refs/remotes/${REMOTE_PUBLISH}/${PATCH_BRANCH}" 2>/dev/null \
  || git fetch "$REMOTE_PUBLISH" 2>/dev/null || true
git fetch "$REMOTE_PUBLISH" "+refs/heads/${PUBLISH_BRANCH}:refs/remotes/${REMOTE_PUBLISH}/${PUBLISH_BRANCH}" 2>/dev/null || true

UPSTREAM_REF="${REMOTE_UPSTREAM}/${UPSTREAM_BRANCH}"
UPSTREAM_SHA=$(git rev-parse "$UPSTREAM_REF")
PATCH_REF=$(resolve_patch_ref)

echo ">>> Upstream:  $(git log -1 --oneline "$UPSTREAM_REF")"
echo ">>> Patch from: ${PATCH_REF} ($(git log -1 --oneline "$PATCH_REF" 2>/dev/null || echo '?'))"
if git rev-parse --verify -q "${REMOTE_PUBLISH}/${PUBLISH_BRANCH}" >/dev/null; then
  echo ">>> Publish:   $(git log -1 --oneline "${REMOTE_PUBLISH}/${PUBLISH_BRANCH}")"
fi

read -rp "Rebuild ${REMOTE_PUBLISH}/${PUBLISH_BRANCH} from ${UPSTREAM_REF} + ${PATCH_FILE}, then refresh ${PATCH_BRANCH}? [y/N] " confirm
[[ "$confirm" == "y" || "$confirm" == "Y" ]] || { echo "Aborted."; exit 1; }

SAVED=$(git branch --show-current 2>/dev/null || true)
WORKDIR=$(git rev-parse --show-toplevel)
TMP_PATCH=$(mktemp)
TMP_NEW_PATCH=$(mktemp)
cleanup() { rm -f "$TMP_PATCH" "$TMP_NEW_PATCH"; }
trap cleanup EXIT

git show "${PATCH_REF}:${PATCH_FILE}" >"$TMP_PATCH" \
  || die "blob ${PATCH_FILE} not found on ${PATCH_REF}"

echo ">>> Resetting local ${PUBLISH_BRANCH} to ${UPSTREAM_REF}"
git switch -C "$PUBLISH_BRANCH" "$UPSTREAM_REF"

echo ">>> Applying ${PATCH_FILE}"
# Apply on a clean upstream tree. --index stages changes for the single commit.
if ! git apply --index --whitespace=nowarn "$TMP_PATCH"; then
  echo "error: git apply failed. Working tree is ${PUBLISH_BRANCH} @ ${UPSTREAM_REF} with a partial apply." >&2
  echo "       Inspect with git status; restore clean with:" >&2
  echo "         git switch -C ${PUBLISH_BRANCH} ${UPSTREAM_REF}" >&2
  exit 1
fi

if [[ -z "$(git diff --cached --name-only)" && -z "$(git ls-files --others --exclude-standard)" ]]; then
  die "patch applied but produced no changes; is ${PATCH_FILE} empty or already applied?"
fi

# Ensure any unstaged apply leftovers are included (defensive).
git add -A

echo ">>> Committing single patch commit"
git commit -m "$COMMIT_MSG"
NEW_MAIN_SHA=$(git rev-parse HEAD)
echo ">>> ${PUBLISH_BRANCH} is now $(git log -1 --oneline HEAD) (parent $(git rev-parse --short HEAD^))"

echo ">>> Pushing ${REMOTE_PUBLISH}/${PUBLISH_BRANCH} (--force-with-lease)"
# Uses remote-tracking tips from the fetch above; refuses if someone else moved main.
git push --force-with-lease "$REMOTE_PUBLISH" "${PUBLISH_BRANCH}:${PUBLISH_BRANCH}"

# Optional recovery tag pointing at this built tip
TAG_NAME="outfile/$(git rev-parse --short "$UPSTREAM_SHA")"
if git rev-parse -q --verify "refs/tags/${TAG_NAME}" >/dev/null; then
  echo ">>> Tag ${TAG_NAME} already exists; skipping"
else
  git tag -a "$TAG_NAME" -m "outfile on upstream ${UPSTREAM_SHA}"
  git push "$REMOTE_PUBLISH" "refs/tags/${TAG_NAME}" || echo ">>> warning: tag push failed (non-fatal)"
fi

echo ">>> Regenerating ${PATCH_FILE} against ${UPSTREAM_REF}"
git diff "$UPSTREAM_REF" HEAD >"$TMP_NEW_PATCH"

echo ">>> Updating ${PATCH_BRANCH} (append-only; no force)"
if git rev-parse --verify -q "refs/heads/${PATCH_BRANCH}" >/dev/null; then
  git switch "$PATCH_BRANCH"
elif git rev-parse --verify -q "refs/remotes/${REMOTE_PUBLISH}/${PATCH_BRANCH}" >/dev/null; then
  git switch -C "$PATCH_BRANCH" "${REMOTE_PUBLISH}/${PATCH_BRANCH}"
else
  die "local ${PATCH_BRANCH} missing after push; re-create the branch"
fi

cp "$TMP_NEW_PATCH" "$WORKDIR/$PATCH_FILE"
# Keep a small pointer to the upstream base we last synced against.
{
  echo "upstream_remote=${REMOTE_UPSTREAM}"
  echo "upstream_branch=${UPSTREAM_BRANCH}"
  echo "upstream_sha=${UPSTREAM_SHA}"
  echo "main_sha=${NEW_MAIN_SHA}"
  echo "synced_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "commit_msg=${COMMIT_MSG}"
} >"$WORKDIR/UPSTREAM.txt"

# Refresh copy of this script from main tip when present
if git cat-file -e "${NEW_MAIN_SHA}:sync-main.sh" 2>/dev/null; then
  git show "${NEW_MAIN_SHA}:sync-main.sh" >"$WORKDIR/sync-main.sh"
  chmod +x "$WORKDIR/sync-main.sh"
fi

git add "$PATCH_FILE" UPSTREAM.txt sync-main.sh 2>/dev/null || git add "$PATCH_FILE" UPSTREAM.txt
if [[ -n "$(git status --porcelain)" ]]; then
  git commit -m "sync: outfile.patch against ${UPSTREAM_SHA:0:12} ($(date -u +%Y-%m-%d))"
  git push "$REMOTE_PUBLISH" "HEAD:refs/heads/${PATCH_BRANCH}"
  echo ">>> Pushed ${REMOTE_PUBLISH}/${PATCH_BRANCH} $(git log -1 --oneline)"
else
  echo ">>> ${PATCH_FILE} unchanged; no patch-branch commit"
fi

if [[ -n "${SAVED}" && "${SAVED}" != "${PATCH_BRANCH}" && "${SAVED}" != "${PUBLISH_BRANCH}" ]]; then
  git switch "$SAVED" || true
elif [[ -n "${SAVED}" ]]; then
  git switch "$SAVED" || git switch "$PUBLISH_BRANCH"
else
  git switch "$PUBLISH_BRANCH"
fi

echo ">>> Done."
echo "    ${REMOTE_PUBLISH}/${PUBLISH_BRANCH}: $(git log -1 --oneline "${REMOTE_PUBLISH}/${PUBLISH_BRANCH}" 2>/dev/null || git log -1 --oneline "$PUBLISH_BRANCH")"
echo "    ${REMOTE_PUBLISH}/${PATCH_BRANCH}:   $(git log -1 --oneline "${REMOTE_PUBLISH}/${PATCH_BRANCH}" 2>/dev/null || git log -1 --oneline "$PATCH_BRANCH")"
echo "    tag: ${TAG_NAME}"
