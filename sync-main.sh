#!/usr/bin/env bash
set -euo pipefail

BRANCH="add-outfile-support"

echo ">>> Fetching latest from origin"
git fetch origin main

echo ">>> Latest commit on $BRANCH:"
git log -1 --oneline "$BRANCH"
echo ">>> Latest origin/main:"
git log -1 --oneline origin/main

read -rp "Cherry-pick ${BRANCH} tip onto origin/main and push to kardianos/main? [y/N] " confirm
[[ "$confirm" != "y" && "$confirm" != "Y" ]] && echo "Aborted." && exit 1

SAVED=$(git branch --show-current)

echo ">>> Resetting local main to origin/main"
git switch -C main origin/main

echo ">>> Cherry-picking latest commit from $BRANCH"
git cherry-pick "$BRANCH"

echo ">>> Pushing main to kardianos"
git push kardianos main --force

git switch "$SAVED"

echo ">>> Done. kardianos/main now:"
git log -2 --oneline kardianos/main
