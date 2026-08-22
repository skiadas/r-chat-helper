# syntax=docker/dockerfile:1

# The binary is built on the GitHub Actions runner (see build-image.yml), then
# copied into this minimal runtime image. Nothing compiles here or on the server.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates && addgroup -S app && adduser -S -G app app
RUN mkdir -p /data && chown -R app:app /data
USER app
WORKDIR /app
COPY control-plane/dist/r-chat-helper /app/r-chat-helper
ENV RC_DB=/data/r-chat-helper.db
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/app/r-chat-helper"]
CMD ["serve"]