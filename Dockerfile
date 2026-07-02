# ---- Build stage ----
FROM golang:1.22-alpine AS builder 

WORKDIR /app 

COPY go.mod ./
RUN go mod download 

COPY main.go ./

RUN CGO_ENABLED=0 GOOS=linux go build -o /app/server main.go 

# ---- Run stage ----
FROM alpine:3.19 

RUN apk --no-cache add ca-certificates

WORKDIR /root/ 

COPY --from=builder /app/server .

EXPOSE 8080

CMD ["./server"]