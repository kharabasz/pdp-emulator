# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build
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
