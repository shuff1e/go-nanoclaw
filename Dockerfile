FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown

RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w \
      -X main.version=${VERSION} \
      -X main.commit=${COMMIT} \
      -X main.buildTime=${BUILD_TIME}" \
    -o /nanoclaw ./cmd/nanoclaw/

FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -H nanoclaw

COPY --from=builder /nanoclaw /usr/local/bin/nanoclaw

USER nanoclaw
WORKDIR /home/nanoclaw

EXPOSE 8765

ENTRYPOINT ["nanoclaw"]
CMD ["serve"]
