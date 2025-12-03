FROM --platform=$BUILDPLATFORM golang:1.21-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o zai-proxy .

FROM alpine:latest

WORKDIR /app

COPY --from=builder /app/zai-proxy .

EXPOSE 8000

CMD ["./zai-proxy"]
