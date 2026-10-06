# syntax=docker/dockerfile:1

# ---- CSS: Tailwind scans the templ files for class names ----
FROM debian:bookworm-slim AS css
ARG TAILWIND_VERSION=v4.3.3
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates && rm -rf /var/lib/apt/lists/*
RUN set -eu; arch=$([ "$TARGETARCH" = "arm64" ] && echo arm64 || echo x64); \
    curl -fsSL -o /usr/local/bin/tailwindcss \
      "https://github.com/tailwindlabs/tailwindcss/releases/download/${TAILWIND_VERSION}/tailwindcss-linux-${arch}"; \
    chmod +x /usr/local/bin/tailwindcss
WORKDIR /src
COPY web/input.css web/input.css
COPY internal internal
RUN tailwindcss -i web/input.css -o /app.css --minify

# ---- Go build (templ and sqlc output is committed, so no generators are needed) ----
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=css /app.css web/static/app.css
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

# ---- Runtime ----
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /out/migrate /
USER nonroot:nonroot
EXPOSE 8080
ENV APP_ENV=production HTTP_ADDR=:8080
ENTRYPOINT ["/server"]
