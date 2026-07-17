# Build stage runs natively on the build machine (BUILDPLATFORM) and lets Go
# cross-compile for the target platform — heavy steps (mod download / compile)
# never go through QEMU when building cross-arch (e.g. arm64 Mac → amd64).
FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

# Define the base directory for the application as an environment variable
ENV SERVER_DIR=/openim-server

# Set the working directory inside the container based on the environment variable
WORKDIR $SERVER_DIR

# Set the Go proxy to improve dependency resolution speed
# ENV GOPROXY=https://goproxy.io,direct

# Copy all files from the current directory into the container
COPY . .

RUN go mod download

# Mage is needed twice: a native binary to run `mage build` in this stage, and a
# target-arch binary shipped into the runtime image (cross `go install` drops it
# under /go/bin/${GOOS}_${GOARCH}/; when build==target it stays in /go/bin).
RUN go install github.com/magefile/mage@v1.15.0 && \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go install github.com/magefile/mage@v1.15.0 && \
    mkdir -p /out && \
    if [ -f "/go/bin/${TARGETOS}_${TARGETARCH}/mage" ]; then \
      cp "/go/bin/${TARGETOS}_${TARGETARCH}/mage" /out/mage; \
    else \
      cp /go/bin/mage /out/mage; \
    fi

# Cross-compile all binaries for the target platform (gomake reads PLATFORMS and
# emits to _output/bin/platforms/${TARGETOS}/${TARGETARCH}/, which is exactly
# where `mage start` looks at runtime via runtime.GOOS/GOARCH)
RUN PLATFORMS="${TARGETOS}_${TARGETARCH}" mage build

# Using Alpine Linux with Go environment for the final image
FROM golang:1.22-alpine

# Install necessary packages, such as bash
RUN apk add --no-cache bash

# Set the environment and work directory
ENV SERVER_DIR=/openim-server
WORKDIR $SERVER_DIR


# Copy the compiled binaries and mage (target-arch build) from the builder image
COPY --from=builder $SERVER_DIR/_output $SERVER_DIR/_output
COPY --from=builder $SERVER_DIR/config $SERVER_DIR/config
COPY --from=builder /out/mage /usr/local/bin/mage
COPY --from=builder $SERVER_DIR/magefile_windows.go $SERVER_DIR/
COPY --from=builder $SERVER_DIR/magefile_unix.go $SERVER_DIR/
COPY --from=builder $SERVER_DIR/magefile.go $SERVER_DIR/
COPY --from=builder $SERVER_DIR/start-config.yml $SERVER_DIR/
COPY --from=builder $SERVER_DIR/go.mod $SERVER_DIR/
COPY --from=builder $SERVER_DIR/go.sum $SERVER_DIR/

RUN go get github.com/openimsdk/gomake@v0.0.15-alpha.5

# Set the command to run when the container starts
ENTRYPOINT ["sh", "-c", "mage start && tail -f /dev/null"]
