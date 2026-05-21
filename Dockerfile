FROM golang:1.22-bookworm

WORKDIR /app

# Install gcc for CGO (required by go-sqlite3)
RUN apt-get update && apt-get install -y gcc && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN CGO_ENABLED=1 go mod download

COPY . .
RUN CGO_ENABLED=1 go build -o popquiz ./cmd/server/

EXPOSE 8080

CMD ["./popquiz"]