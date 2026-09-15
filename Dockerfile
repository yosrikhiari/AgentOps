# syntax=docker/dockerfile:1
# Multi-stage: build a static binary, ship it on a minimal non-root image.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/agentops .

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/agentops /app/agentops
# migrations + golden files + corpus chunks are read from the working directory at runtime
COPY migrations /app/migrations
COPY evals/golden /app/evals/golden
COPY evals/corpus/clean /app/evals/corpus/clean
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/app/agentops"]
