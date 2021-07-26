VERSION ?= 0.2.0
REPO ?= gcr.io/cf-pks-releng-environments/tm/kmotion
# Image URL to use all building/pushing image targets
IMG ?= ${REPO}:${VERSION}
GOOS ?= `go env GOOS`
GOBIN ?= ${GOPATH}/bin
GO111MODULE = auto

# Run go fmt against code
fmt:
	go fmt ./pkg/... ./cmd/...

# Run go vet against code
vet:
	go vet -structtag=false ./pkg/... ./cmd/...

# Build manager binary
kmotion: fmt vet
	go build -a -o kmotion main.go

# Build the docker image
docker-build:
	docker build . -t ${IMG}

# Push the docker image
docker-push:
	docker push ${IMG}
