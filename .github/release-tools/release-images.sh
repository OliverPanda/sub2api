#!/usr/bin/env bash
set -euo pipefail
: "${RELEASE_VERSION:?}" "${RELEASE_SHA:?}" "${GITHUB_REPOSITORY:?}" "${RUNNER_TEMP:?}"

owner=${GITHUB_REPOSITORY%%/*}
owner_lower=$(printf '%s' "$owner" | tr '[:upper:]' '[:lower:]')
source_url="https://github.com/$GITHUB_REPOSITORY"
app_registries=("ghcr.io/${owner_lower}/sub2api")
if [[ ${SIMPLE_RELEASE:-false} != true && ${DOCKERHUB_USERNAME:-skip} != skip ]]; then
  app_registries+=("${DOCKERHUB_USERNAME}/sub2api")
fi
arches=(amd64 arm64)
if [[ ${SIMPLE_RELEASE:-false} == true ]]; then arches=(amd64); fi

build_app() {
  local arch=$1 registry
  local -a args=(
    --platform "linux/$arch"
    --file ".release-context/$arch/Dockerfile"
    --label "org.opencontainers.image.version=$RELEASE_VERSION"
    --label "org.opencontainers.image.revision=$RELEASE_SHA"
    --label "org.opencontainers.image.source=$source_url"
  )
  for registry in "${app_registries[@]}"; do
    args+=(--tag "$registry:$RELEASE_VERSION-$arch")
  done
  if [[ ${SIMPLE_RELEASE:-false} == true ]]; then
    args+=(--tag "$registry:$RELEASE_VERSION" --tag "$registry:latest")
  fi
  if [[ ${DRY_RUN:-false} == true ]]; then
    args+=(--output "type=oci,dest=$RUNNER_TEMP/sub2api-$arch.oci.tar")
  else
    args+=(--push)
  fi
  docker buildx build "${args[@]}" ".release-context/$arch"
}

for arch in "${arches[@]}"; do
  build_app "$arch"
done

if [[ ${DRY_RUN:-false} != true && ${SIMPLE_RELEASE:-false} != true ]]; then
  major=${RELEASE_VERSION%%.*}
  minor=${RELEASE_VERSION#*.}; minor=${minor%%.*}
  for registry in "${app_registries[@]}"; do
    docker buildx imagetools create \
      --tag "$registry:$RELEASE_VERSION" --tag "$registry:latest" \
      --tag "$registry:$major.$minor" --tag "$registry:$major" \
      "$registry:$RELEASE_VERSION-amd64" "$registry:$RELEASE_VERSION-arm64"
  done
fi
