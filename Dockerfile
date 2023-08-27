# syntax=docker/dockerfile:1

FROM golang:1.20.5 AS build

WORKDIR /app

COPY Makefile go.mod go.sum ./
RUN go mod download

COPY cmd ./

RUN make CGO_ENABLED=0 build

# TODO: verify image with cosign
FROM gcr.io/distroless/static-debian11
COPY --from=build /app/out/bin/dtn-ipfs /
CMD ["/dtn-ipfs"]
