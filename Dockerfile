# syntax=docker/dockerfile:1.7

# ---- build ------------------------------------------------------------------
FROM golang:1.25-alpine AS build
WORKDIR /src

# Cache module download separately from source compilation.
COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY . .

# CGO disabled → static binary. trimpath / -s -w → small image.
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/translation-layer ./cmd/translation-layer

# ---- runtime ----------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/translation-layer /usr/local/bin/translation-layer
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/translation-layer"]
