# Telegram Lead Bot

Telegram lead parser and auction bot written in Go.

## Structure

- `lidohod/` - production parser, frontend bot, Docker setup.
- `get_id/` - local helper scripts for discovering Telegram chat IDs and lead sources.

## Local Setup

1. Copy environment templates:

```sh
cp lidohod/.env.example lidohod/.env
cp get_id/.env.example get_id/.env
```

2. Fill Telegram, bot, payment and AI API credentials in `.env` files.

3. Start services:

```sh
cd lidohod
docker compose up -d --build
```

Runtime files under `data/`, Telegram sessions, databases, reports and `.env` files are intentionally ignored and must not be committed.
