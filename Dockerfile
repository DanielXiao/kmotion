# Build the manager binary
FROM golang:1.15 as builder

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY pkg/    pkg/
COPY cmd/    cmd/
COPY main.go main.go

# Build
RUN GOOS=linux GOARCH=amd64 GO111MODULE=on go build -a -o kmotion main.go

FROM photon:4.0
RUN tdnf distro-sync --refresh -y && \
    tdnf install shadow -y && \
    tdnf info installed && \
    tdnf clean all
# Create a tanzu-migrator user so the application doesn't run as root.
RUN groupadd -g 10000 tanzu-migrator && \
    useradd -u 10000 -g tanzu-migrator -s /sbin/nologin -c "tanzu-migrator user" tanzu-migrator
USER tanzu-migrator
WORKDIR /home/tanzu-migrator

COPY --from=builder /workspace/kmotion .

ENTRYPOINT ["/home/tanzu-migrator/kmotion"]
