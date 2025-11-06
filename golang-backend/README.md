Flux Panel Go Backend (Gin + GORM)

Overview
- This is a Go implementation of the Spring Boot backend under `springboot-backend`.
- HTTP framework: `gin`
- DB: `gorm` with MySQL driver
- Mirrors core endpoints and business rules where feasible. Complex integrations (Gost websockets, captcha vendor) are stubbed.

Environment
- `DB_HOST` (e.g. 127.0.0.1)
- `DB_NAME`
- `DB_USER`
- `DB_PASSWORD`
- `DB_PORT` (default 3306)
- `JWT_SECRET` (required)
- `PORT` (default 6365)

Dotenv
- The server auto-loads environment variables from `.env` if present.
- Supported formats: `KEY=VALUE`, `export KEY=VALUE`, quotes are stripped.
- It tries `.env`, `../.env`, and `../../.env`.

Run
- `go run ./cmd/server`

Notes
- Models match existing tables. If schema differs, adjust GORM tags accordingly.
- Gost-related operations in services are currently no-ops returning OK.

Docker
- One image bundles frontend (vite-frontend) and backend (golang-backend).
- Dockerfile at repo root builds both and serves frontend via Gin static files.
- Build: `docker build -t network-panel:latest .`
- Run: `PORT=6365 JWT_SECRET=... DB_HOST=... DB_NAME=... DB_USER=... DB_PASSWORD=... docker run -p 6365:6365 --name network-panel --rm network-panel:latest`
- Or use `./install_docker.sh up` (reads .env if present, see script help).
