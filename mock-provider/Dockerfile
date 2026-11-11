FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY mock-provider/ ./mock-provider/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mock-provider ./mock-provider

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mock-provider /mock-provider
EXPOSE 8081
USER nonroot
ENTRYPOINT ["/mock-provider"]
