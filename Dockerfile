# syntax=docker/dockerfile:1

# ---------- build stage ----------
# Toolchain tracks go.mod (go 1.27.x). Alpine keeps the build image small.
FROM golang:1.27-alpine AS build
WORKDIR /src

# Download modules in their own layer so it is cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

# Static, stripped binaries: CGO off so they run on distroless/static, -s -w drop
# debug info to shrink them. The API ships with its nightly jobs (see
# deploy/nightly.sh) so the jobs always match the live API. The migrate tool is a
# local dev command and deliberately never ships to production.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/ ./cmd/api ./cmd/coliked ./cmd/notify ./cmd/opendata ./cmd/seed-catalog

# ---------- runtime stage ----------
# distroless/static-nonroot: no shell, no package manager, runs as a non-root
# user, and already ships CA certificates so outbound HTTPS to Supabase and
# TMDB works. The final image is just the binaries — nothing to exploit a shell in.
# The :nonroot tag runs as an unprivileged user (uid 65532) by default.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/api /out/coliked /out/notify /out/opendata /out/seed-catalog /

# Documents the listen port; SERVER_PORT must match. The image publishes nothing
# to the host on its own — compose decides what (if anything) is exposed.
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/api"]
