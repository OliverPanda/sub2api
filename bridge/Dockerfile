FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/bridge ./cmd/bridge

FROM alpine:3.22
ARG VERSION
ARG REVISION
ARG SOURCE
LABEL org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$REVISION \
      org.opencontainers.image.source=$SOURCE
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 65532 bridge \
    && adduser -S -D -H -u 65532 -G bridge bridge
WORKDIR /app
COPY --from=build /out/bridge /app/bridge
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/app/bridge"]
