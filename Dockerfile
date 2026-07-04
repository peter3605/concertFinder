FROM node:20-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web ./
RUN npm run build

FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download || true
COPY . .
# Overwrite the placeholder SPA with the real build output so go:embed picks
# it up.
RUN rm -rf internal/http/spa/static/*
COPY --from=web /web/dist/ internal/http/spa/static/
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/server ./cmd/server

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/server /server
COPY --from=build /src/migrations /migrations
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/server"]
