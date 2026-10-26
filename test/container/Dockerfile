# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM golang:1.26.7-alpine AS build

WORKDIR /workspace

COPY go.mod go.sum ./

RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY test/container ./test/container

ARG TARGETOS=linux
ARG TARGETARCH

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath -o /out/redfish-mock ./test/container/mock

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath -o /out/redfish-verifier ./test/container/verify

FROM gcr.io/distroless/static-debian12:nonroot AS mock

COPY --from=build /out/redfish-mock /usr/local/bin/redfish-mock

USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/redfish-mock"]

FROM gcr.io/distroless/static-debian12:nonroot AS verifier

COPY --from=build /out/redfish-verifier /usr/local/bin/redfish-verifier

USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/redfish-verifier"]
