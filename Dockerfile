# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:eb61c3873c97416fff0116553fff65d67a3549f72e11d9758849a689cdd8b523 AS build
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
