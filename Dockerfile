# ---- build stage ----
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# client-go is pure Go; build a static binary so it runs on a minimal base.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/kelp ./cmd/kelp

# ---- kubectl stage ----
# kelp shells out to kubectl for command passthrough, so the runtime image
# needs it. Pull a pinned static kubectl from the official bitnami image.
FROM bitnami/kubectl:1.30 AS kubectl

# ---- runtime stage ----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=kubectl /opt/bitnami/kubectl/bin/kubectl /usr/local/bin/kubectl
COPY --from=build /out/kelp /usr/local/bin/kelp
ENTRYPOINT ["/usr/local/bin/kelp"]
