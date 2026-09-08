# The Go binary is built by CircleCI (architect/go-build) and attached to the
# build context as <binary>-<os>-<arch>; this image only assembles the runtime.
# For a local build, produce the binary first:
#   CGO_ENABLED=0 go build -o mcp-template-linux-amd64 .
FROM gcr.io/distroless/static-debian12:nonroot
ARG TARGETOS
ARG TARGETARCH
COPY mcp-template-${TARGETOS}-${TARGETARCH} /usr/local/bin/mcp-template
USER nonroot:nonroot
EXPOSE 8080 9091
ENTRYPOINT ["/usr/local/bin/mcp-template"]
CMD ["serve"]
