# Build from the repository root:
#   docker build -f services/realtime/Dockerfile .
# For another CPU (the Oracle server is ARM):
#   docker buildx build --platform linux/arm64 -f services/realtime/Dockerfile .

# 1. Build a static binary. The build stage always runs on the build machine's own CPU
#    ($BUILDPLATFORM) and Go cross-compiles for the target ($TARGETOS/$TARGETARCH), so an ARM
#    image is built without emulating ARM.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src

# Download dependencies first so this layer is cached between builds
COPY go.mod go.sum ./
RUN go mod download

COPY pkg ./pkg
COPY services/realtime ./services/realtime
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/realtime ./services/realtime/cmd

# 2. Small runtime image: only the binary, no shell, runs as a non-root user
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/realtime /realtime
EXPOSE 8083
USER nonroot:nonroot
ENTRYPOINT ["/realtime"]
