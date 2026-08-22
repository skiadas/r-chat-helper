# syntax=docker/dockerfile:1

# Build stage: compile the control-plane binary (also embeds the chat UI).
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY control-plane/ /src/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/r-chat-helper ./cmd/r-chat-helper

# Runtime stage: minimal image with CA certs (webfetch + OIDC need TLS).
FROM alpine:3.20
RUN apk add --no-cache ca-certificates && addgroup -S app && adduser -S -G app app
USER app
WORKDIR /app
COPY --from=build /out/r-chat-helper /app/r-chat-helper
ENV RC_DB=/data/r-chat-helper.db
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/app/r-chat-helper"]
CMD ["serve"]