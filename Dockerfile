FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build

RUN apk add --no-cache git

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o webhook -ldflags '-w -extldflags "-static"' .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=build /workspace/webhook /usr/local/bin/webhook
USER 65532:65532
ENTRYPOINT ["webhook"]
