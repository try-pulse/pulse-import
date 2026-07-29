FROM alpine:3.21
RUN apk add --no-cache ca-certificates
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/pulse-import /usr/local/bin/pulse-import
ENTRYPOINT ["pulse-import"]
