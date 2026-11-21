# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /pdp-emulator .

FROM scratch
COPY --from=build /pdp-emulator /pdp-emulator
EXPOSE 7000
USER 65534:65534
HEALTHCHECK --interval=10s --timeout=3s --retries=3 CMD ["/pdp-emulator", "healthcheck"]
ENTRYPOINT ["/pdp-emulator"]
