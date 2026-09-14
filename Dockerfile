FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o /copilot ./cmd/copilot

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /copilot /usr/local/bin/copilot
COPY data/knowledge /app/data/knowledge
ENV COPILOT_INDEX_PATH=/tmp/index.json COPILOT_LISTEN=:8080
EXPOSE 8080
ENTRYPOINT ["copilot"]
CMD ["serve"]
