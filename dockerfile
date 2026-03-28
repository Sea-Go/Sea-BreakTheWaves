FROM debian:bookworm-slim

WORKDIR /app

RUN echo 'Acquire::ForceIPv4 "true";' > /etc/apt/apt.conf.d/99force-ipv4 \
    && sed 's+://.*/d+://mirrors.aliyun.com/d+g' /etc/apt/sources.list.d/debian.sources -i \
    && apt-get -o Acquire::Retries=3 -o Acquire::http::Timeout=20 update \
    && apt-get install -y --no-install-recommends ca-certificates -o APT::Status-Fd=1 \
       | awk -F: '/^pmstatus:/ { printf("[apt] %3s%%  %s\n", $3, $4); fflush(); next } { print }' \
    && update-ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --chmod=755 breakthewaves /app/breakthewaves
COPY config.yaml /app/config.yaml

EXPOSE 20721

CMD ["/app/breakthewaves"]
