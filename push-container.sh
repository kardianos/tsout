#!/usr/bin/env bash
set -euo pipefail

IMAGE="ghcr.io/kardianos/tsout"
TAG="${1:-latest}"

echo ">>> Building $IMAGE:$TAG"
docker build -t "$IMAGE:$TAG" -f Containerfile .

GIT_SHA=$(git rev-parse --short HEAD)
docker tag "$IMAGE:$TAG" "$IMAGE:$GIT_SHA"

echo ">>> Pushing $IMAGE:$TAG"
docker push "$IMAGE:$TAG"

echo ">>> Pushing $IMAGE:$GIT_SHA"
docker push "$IMAGE:$GIT_SHA"

echo ">>> Done. Pushed $IMAGE:$TAG and $IMAGE:$GIT_SHA"
