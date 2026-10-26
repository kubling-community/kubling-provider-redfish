# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM golang:1.26.7-alpine AS build

WORKDIR /workspace

COPY go.mod go.sum ./

RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ARG TARGETOS=linux
ARG TARGETARCH

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath -ldflags="-s -w" \
    -o /out/kubling-provider-redfish ./cmd/kubling-provider-redfish

FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="Kubling Redfish Provider" \
      org.opencontainers.image.description="Redfish infrastructure provider for Kubling" \
      org.opencontainers.image.source="https://github.com/kubling-community/kubling-provider-redfish" \
      org.opencontainers.image.licenses="Apache-2.0"

COPY --from=build /out/kubling-provider-redfish /usr/local/bin/kubling-provider-redfish
COPY LICENSE /licenses/LICENSE

ENV KUBLING_REDFISH_CONFIG=/etc/kubling/provider.yaml

EXPOSE 50051

USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/kubling-provider-redfish"]
CMD ["-listen", ":50051"]
