# Base images are pinned by digest, tag included so the reference still says
# which version it is. The deploy builds this image on the instance, so a
# floating tag means every deploy is also an unreviewed base-image upgrade: the
# thing that changed between a working build and a broken one is not in the
# diff, and the build that breaks is the one already running under `git reset
# --hard` on the box.
#
# Re-resolve with:
#   docker buildx imagetools inspect <name:tag> --format '{{.Manifest.Digest}}'
#
# That command returns the multi-arch *index* digest, which is what belongs
# here. `docker images --digests` after a local pull gives the digest for your
# own architecture instead, and the build host is arm64 (t4g.small) — pinning a
# per-arch digest would fail the build outright on a mismatched host, or worse,
# silently build for the wrong one.
FROM node:26-alpine@sha256:2d984a15c9b54fd0aeb608b8e0d0d83529eb34d2966db27a1fb4f1edc3d298a3 AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web ./
RUN npm run build

FROM golang:1.25-alpine@sha256:1ae0735f00daffa3aaf1363a5184c0d2dc55c78e3db4ec70241cdac97bf84b59 AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download || true
COPY . .
# Overwrite the placeholder SPA with the real build output so go:embed picks
# it up.
RUN rm -rf internal/http/spa/static/*
COPY --from=web /web/dist/ internal/http/spa/static/
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/server ./cmd/server

FROM gcr.io/distroless/static-debian12@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2
COPY --from=build /out/server /server
COPY --from=build /src/migrations /migrations
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/server"]
