# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.25-alpine@sha256:1ae0735f00daffa3aaf1363a5184c0d2dc55c78e3db4ec70241cdac97bf84b59 AS build
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
