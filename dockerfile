FROM debian:bookworm-slim

WORKDIR /app

COPY breakthewaves /app/breakthewaves
COPY config.yaml /app/config.yaml

RUN chmod +x /app/breakthewaves

EXPOSE 20721

CMD ["/app/breakthewaves"]