# Stage 1: Build frontend
FROM node:20-alpine AS frontend-builder
WORKDIR /app/frontend
COPY frontend/package.json frontend/package-lock.json* ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

# Stage 2: Build Go server
FROM golang:1.22-alpine AS go-builder
WORKDIR /app/server
COPY server/go.mod server/go.sum* ./
RUN go mod download
COPY server/ ./
# Copy built frontend into static/ so the server can embed it
COPY --from=frontend-builder /app/frontend/../server/static ./static
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /collab-editor .

# Stage 3: Minimal runtime image
FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=go-builder /collab-editor /collab-editor
COPY --from=go-builder /app/server/static /static

EXPOSE 8080
ENV PORT=8080

ENTRYPOINT ["/collab-editor"]
