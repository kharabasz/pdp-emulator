# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.24-alpine@sha256:8bee1901f1e530bfb4a7850aa7a479d17ae3a18beb6e09064ed54cfd245b7191 AS build
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
