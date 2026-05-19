FROM python:3.12-slim
COPY --from=ghcr.io/astral-sh/uv:latest /uv /usr/local/bin/uv
WORKDIR /app
COPY pyproject.toml .
RUN uv sync --no-dev
COPY server.py .
EXPOSE 8020
CMD ["uv", "run", "python", "server.py"]
