FROM golang:1.25-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -trimpath -ldflags="-s -w" -o /out/factory-api ./cmd/factory-api \
    && CGO_ENABLED=0 GOOS=linux go build -mod=vendor -trimpath -ldflags="-s -w" -o /out/factory-worker ./cmd/factory-worker \
    && CGO_ENABLED=0 GOOS=linux go build -mod=vendor -trimpath -ldflags="-s -w" -o /out/factory-migrate ./cmd/factory-migrate

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S factory \
    && adduser -S -G factory -u 10001 factory
COPY --from=build /out/factory-* /usr/local/bin/
USER 10001
ENTRYPOINT ["/usr/local/bin/factory-api"]
