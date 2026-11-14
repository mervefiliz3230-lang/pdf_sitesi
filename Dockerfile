FROM golang:1.27.3-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
RUN CGO_ENABLED=0 go build -trimpath -o /pdf-site .

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends libreoffice-writer libreoffice-calc libreoffice-impress fonts-dejavu fonts-liberation ca-certificates && rm -rf /var/lib/apt/lists/*
RUN useradd --create-home --uid 10001 app
WORKDIR /app
COPY --from=build /pdf-site /app/pdf-site
COPY index.html /app/index.html
RUN mkdir /app/uploads && chown -R app:app /app
USER app
ENV PORT=8080
EXPOSE 8080
CMD ["/app/pdf-site"]
