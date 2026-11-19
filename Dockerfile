# syntax=docker/dockerfile:1

# ---------- build stage ----------
# Toolchain tracks go.mod (go 1.27.x). Alpine keeps the build image small.
FROM golang:1.27-alpine AS build
WORKDIR /src

# Download modules in their own layer so it is cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

# The plot job's embedding model, bge-small-en-v1.5 (MIT), pinned to one
# revision and checked against its hash. config.json sets the 512-token limit
# the tokenizer truncates to. This layer only changes with the model, so a new
# image normally reuses it.
ARG BGE_REV=5c38ec7c405ec4b44b94cc5a9bb96e735b38267a
ADD --checksum=sha256:828e1496d7fabb79cfa4dcd84fa38625c0d3d21da474a00f08db0f559940cf35     https://huggingface.co/BAAI/bge-small-en-v1.5/resolve/${BGE_REV}/onnx/model.onnx /out/model/model.onnx
ADD --checksum=sha256:d241a60d5e8f04cc1b2b3e9ef7a4921b27bf526d9f6050ab90f9267a1f9e5c66     https://huggingface.co/BAAI/bge-small-en-v1.5/resolve/${BGE_REV}/tokenizer.json /out/model/tokenizer.json
RUN echo '{"max_position_embeddings": 512}' > /out/model/config.json && chmod 0644 /out/model/*

# Static, stripped binaries: CGO off so they run on distroless/static, -s -w drop
# debug info to shrink them. The API ships with its nightly jobs (see
# deploy/nightly.sh) so the jobs always match the live API. The migrate tool is a
# local dev command and deliberately never ships to production.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/ ./cmd/api ./cmd/coliked ./cmd/notify ./cmd/opendata ./cmd/seed-catalog
# The plot job alone builds with Go's SIMD experiment, which makes the model
# about four times faster. It checks the CPU at run time and falls back to
# plain Go on a CPU without AVX2.
RUN CGO_ENABLED=0 GOOS=linux GOEXPERIMENT=simd go build -ldflags="-s -w" -o /out/ ./cmd/plots

# The licenses of everything the image ships, in /licenses: Go's own, every
# module compiled into the binaries above (same settings, so the same
# modules), and the model's. The binaries are stripped, so this is where
# those notices travel. pipefail so a failed go list fails the build.
RUN set -o pipefail \
 && mkdir /out/licenses \
 && cp "$(go env GOROOT)/LICENSE" /out/licenses/go.txt \
 && cp deploy/licenses/*.txt /out/licenses/ \
 && F='{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}' \
 && { CGO_ENABLED=0 GOOS=linux go list -deps -f "$F" ./cmd/api ./cmd/coliked ./cmd/notify ./cmd/opendata ./cmd/seed-catalog \
   && CGO_ENABLED=0 GOOS=linux GOEXPERIMENT=simd go list -deps -f "$F" ./cmd/plots; } \
  | sh deploy/licenses.sh /out/licenses/go-modules.txt

# ---------- runtime stage ----------
# distroless/static-nonroot: no shell, no package manager, runs as a non-root
# user, and already ships CA certificates so outbound HTTPS to Supabase and
# TMDB works. The final image is just the binaries — nothing to exploit a shell in.
# The :nonroot tag runs as an unprivileged user (uid 65532) by default.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/model /model
COPY --from=build /out/licenses /licenses
COPY --from=build /out/api /out/coliked /out/notify /out/opendata /out/plots /out/seed-catalog /

# Documents the listen port; SERVER_PORT must match. The image publishes nothing
# to the host on its own — compose decides what (if anything) is exposed.
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/api"]
