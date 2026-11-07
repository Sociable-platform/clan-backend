# Build from the repository root:
#   docker build -f services/social/Dockerfile .

# 1. Build a static binary
FROM golang:1.25-alpine AS build
WORKDIR /src

# Download dependencies first so this layer is cached between builds
COPY go.mod go.sum ./
RUN go mod download

COPY pkg ./pkg
COPY services/social ./services/social
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/social ./services/social/cmd

# 2. Small runtime image: only the binary, no shell, runs as a non-root user
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/social /social
EXPOSE 8082
USER nonroot:nonroot
ENTRYPOINT ["/social"]
