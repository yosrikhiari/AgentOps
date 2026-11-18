# syntax=docker/dockerfile:1
# Multi-stage: build a static binary, ship it on a minimal non-root image.
#
# Base images are pinned by digest as well as tag: a tag is mutable, and this project's
# whole release story is that a compromised dependency cannot quietly change the artefact.
# Dependabot's docker ecosystem bumps both the tag and the digest.
FROM golang:1.27-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tower .

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
WORKDIR /app
COPY --from=build /out/tower /app/tower
# migrations + golden files + corpus chunks are read from the working directory at runtime
COPY migrations /app/migrations
COPY evals/golden /app/evals/golden
COPY evals/corpus/clean /app/evals/corpus/clean
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/app/tower"]
