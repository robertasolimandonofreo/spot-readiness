# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/spot-readiness ./cmd/spot-readiness

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.source="https://github.com/robertasolimandonofreo/spot-readiness" \
      org.opencontainers.image.description="Find out which Kubernetes workloads can safely run on Spot" \
      org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build /out/spot-readiness /spot-readiness
ENTRYPOINT ["/spot-readiness"]
