# ---- Build stage ----
FROM golang:1.22-alpine AS builder 

WORKDIR /app 

COPY go.mod ./
COPY main.go ./

RUN go mod tidy
RUN go mod download 





RUN CGO_ENABLED=0 GOOS=linux go build -o /app/server main.go 

# ---- Run stage ----
FROM alpine:3.19 

RUN apk --no-cache add ca-certificates

WORKDIR /root/ 

COPY --from=builder /app/server .

COPY migrations ./migrations

EXPOSE 8080

CMD ["./server"]