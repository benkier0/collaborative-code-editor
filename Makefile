.PHONY: dev test build clean

# Run Go server + Vite dev server concurrently (requires 'mprocs' or run in two terminals)
dev:
	@echo "→ Start the Go server:   cd server && go run ."
	@echo "→ Start the frontend:    cd frontend && npm run dev"
	@echo ""
	@echo "Or install mprocs (brew install mprocs) and run: mprocs"

# Run Go OT unit tests
test:
	cd server && go test ./ot/... -v

# Build frontend then Go binary
build:
	cd frontend && npm ci && npm run build
	cd server && go build -o ../collab-editor .

# Docker build
docker:
	docker build -t collab-editor .

# Docker run with local Redis
docker-run:
	docker run -p 8080:8080 -e REDIS_URL=redis://host.docker.internal:6379 collab-editor

clean:
	rm -rf server/static collab-editor
