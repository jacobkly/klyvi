# syntax=docker/dockerfile:1

# ---------- build stage ----------
# Toolchain tracks go.mod (go 1.24.x). Alpine keeps the build image small.
FROM golang:1.24-alpine AS build
WORKDIR /src

# Download modules in their own layer so it is cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

# Static, stripped binary: CGO off so it runs on distroless/static, -s -w drop
# debug info to shrink it. Only the API is built — the migrate tool is a local
# dev command and deliberately never ships to production.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/api ./cmd/api

# ---------- runtime stage ----------
# distroless/static-nonroot: no shell, no package manager, runs as a non-root
# user, and already ships CA certificates so outbound HTTPS to Supabase and
# TMDB works. The final image is just the binary — nothing to exploit a shell in.
# The :nonroot tag runs as an unprivileged user (uid 65532) by default.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/api /api

# Documents the listen port; SERVER_PORT must match. The image publishes nothing
# to the host on its own — compose decides what (if anything) is exposed.
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/api"]
