FROM python:3.12-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential libpq-dev \
    && rm -rf /var/lib/apt/lists/*

RUN pip install --no-cache-dir uv

WORKDIR /app
COPY pyproject.toml ./
COPY src ./src

RUN uv pip install --system --no-cache .

RUN useradd --create-home --uid 1001 agent
USER agent

EXPOSE 8100
CMD ["uvicorn", "agent_api.main:app", "--host", "0.0.0.0", "--port", "8100"]
