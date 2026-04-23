FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=v0.0.0-dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=0 go build \
    -ldflags "-s -w \
      -X main.version=${VERSION} \
      -X main.commit=${COMMIT} \
      -X main.buildDate=${BUILD_DATE}" \
    -o /qspill-controller \
    ./cmd/controller

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /qspill-controller /qspill-controller

USER 65532:65532

ENTRYPOINT ["/qspill-controller"]
