# ---- build stage ----
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# client-go is pure Go; build a static binary so it runs on a minimal base.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/kelper ./cmd/kelper

# ---- runtime stage ----
# kelper shells out to kubectl for command passthrough, so the runtime image
# needs it. Pull a pinned, current kubectl straight from the official release
# bucket (dl.k8s.io) rather than a third-party image.
FROM alpine:3.20
ARG KUBECTL_VERSION=v1.36.2
ARG TARGETARCH=amd64
RUN apk add --no-cache ca-certificates curl \
    && curl -fsSL -o /usr/local/bin/kubectl \
        "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl" \
    && chmod +x /usr/local/bin/kubectl \
    && apk del curl
COPY --from=build /out/kelper /usr/local/bin/kelper
ENTRYPOINT ["/usr/local/bin/kelper"]
