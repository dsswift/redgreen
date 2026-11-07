# syntax=docker/dockerfile:1
# A static binary on distroless. The Orrery SDK is a private module: the
# build fetches it through the caller's SSH agent (docker build --ssh default),
# so no layer keeps a key.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
RUN apk add --no-cache git openssh-client
RUN mkdir -p ~/.ssh && echo 'github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl' >> ~/.ssh/known_hosts \
 && git config --global url."git@github.com:dsswift/orrery".insteadOf https://github.com/dsswift/orrery
WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOPRIVATE=github.com/dsswift/orrery
COPY go.mod go.sum ./
RUN --mount=type=ssh go mod download
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
