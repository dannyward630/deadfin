# Canonical environment for committed golden fixture generation.
FROM golang:1.25.9-bookworm@sha256:298734aec230b5f3e8cee450ce6d7eccc39f1797ba548ee90d57e9803030c6c3

# Freeze ffmpeg and every linked encoder library at one Debian snapshot.
RUN rm -f /etc/apt/sources.list.d/debian.sources \
    && printf '%s\n' \
        'deb [check-valid-until=no] https://snapshot.debian.org/archive/debian/20260728T000000Z bookworm main' \
        'deb [check-valid-until=no] https://snapshot.debian.org/archive/debian/20260728T000000Z bookworm-updates main' \
        'deb [check-valid-until=no] https://snapshot.debian.org/archive/debian-security/20260728T000000Z bookworm-security main' \
        > /etc/apt/sources.list \
    && apt-get update \
    && apt-get install --yes --no-install-recommends ffmpeg=7:5.1.9-0+deb12u1 python3 \
    && rm -rf /var/lib/apt/lists/*

RUN install -d -m 0755 /usr/local/share/jellyport \
    && printf '%s\n' \
        'format=jellyport-fixture-generator-v1' \
        'base=golang:1.25.9-bookworm@sha256:298734aec230b5f3e8cee450ce6d7eccc39f1797ba548ee90d57e9803030c6c3' \
        'snapshot=20260728T000000Z' \
        'ffmpeg=7:5.1.9-0+deb12u1' \
        'platform=linux/amd64' \
        > /usr/local/share/jellyport/fixture-generator-v1 \
    && chmod 0644 /usr/local/share/jellyport/fixture-generator-v1

WORKDIR /work
