FROM mcr.microsoft.com/playwright/python:v1.48.0-jammy

WORKDIR /app

# Build context is the parent services/ directory so we can pull in the
# shared discord_common/ package alongside this service's files.
COPY discord_common/requirements.txt /tmp/req.txt
RUN pip install --no-cache-dir -r /tmp/req.txt

COPY discord_common/ /app/discord_common/
COPY discord-copytrade/ /app/

RUN mkdir -p /app/state

CMD ["python3", "-m", "watcher"]
