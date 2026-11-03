# syntax=docker/dockerfile:1

ARG GO_VERSION=1.26.5

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -mod=readonly -trimpath -ldflags="-s -w" -o /out/app ./cmd/app

FROM build AS test
RUN apk add --no-cache gcc musl-dev
ENV CGO_ENABLED=1
CMD ["go", "test", "-mod=readonly", "-race", "-count=1", "./..."]

FROM gcr.io/distroless/static-debian12:nonroot AS runtime
COPY --from=build /out/app /app
EXPOSE 8080
ENTRYPOINT ["/app"]
