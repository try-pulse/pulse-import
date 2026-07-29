FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY pulse-import /usr/local/bin/pulse-import
ENTRYPOINT ["pulse-import"]
