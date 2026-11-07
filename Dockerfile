# syntax=docker/dockerfile:1
# A static binary on distroless. The Orrery SDK is a private module: the
# build reads it with the token in the `gitauth` secret, for one command only,
# so no layer keeps it.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
RUN apk add --no-cache git
WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOPRIVATE=github.com/dsswift/*
COPY go.mod go.sum ./
RUN --mount=type=secret,id=gitauth \
    if [ -s /run/secrets/gitauth ]; then \
      GIT_CONFIG_COUNT=1 \
      GIT_CONFIG_KEY_0="url.https://x-access-token:$(cat /run/secrets/gitauth)@github.com/dsswift/.insteadOf" \
      GIT_CONFIG_VALUE_0="https://github.com/dsswift/" \
      go mod download; \
    else go mod download; fi
COPY VERSION ./
COPY cmd ./cmd
COPY internal ./internal
RUN version=$(sed 's/ .*//' VERSION) \
 && go build -trimpath -ldflags "-s -w -X main.version=${version}" -o /out/redgreen ./cmd/redgreen \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/redgreen /app/redgreen
COPY orrery.yaml logo.svg /app/
COPY --from=build --chown=nonroot:nonroot /out/data /data
ENV PORT=8080 DATA_DIR=/data
VOLUME /data
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/app/redgreen"]
CMD ["serve"]
