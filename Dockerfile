# den: a personal fx agent in a container.

FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG DEN_VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$DEN_VERSION" -o /out/den ./cmd/den

# whisper-cli transcribes voice messages when DEN_WHISPER_MODEL is set. A
# local build tunes it for this machine's CPU; the published image sets
# WHISPER_NATIVE=OFF, so it runs on any CPU of its architecture, a little
# slower. GCC 12 fails on ggml's arm64 fp16 code, so this uses trixie.
FROM debian:trixie-slim AS whisper
RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential ca-certificates cmake git && \
    rm -rf /var/lib/apt/lists/*
ARG WHISPER_VERSION=v1.9.5
ARG WHISPER_NATIVE=ON
RUN git clone --depth 1 --branch "$WHISPER_VERSION" https://github.com/ggml-org/whisper.cpp /src && \
    cmake -S /src -B /src/build -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF \
        -DGGML_OPENMP=OFF -DWHISPER_BUILD_TESTS=OFF -DGGML_NATIVE="$WHISPER_NATIVE" && \
    cmake --build /src/build -j --target whisper-cli && \
    strip /src/build/bin/whisper-cli

# Lightpanda is a small headless browser the web skill uses for pages that
# need JavaScript.
FROM debian:trixie-slim AS downloads
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl && \
    rm -rf /var/lib/apt/lists/*
ARG LIGHTPANDA_VERSION=1.0.3
RUN curl -fsSL -o /lightpanda \
        "https://github.com/lightpanda-io/browser/releases/download/$LIGHTPANDA_VERSION/lightpanda-$(uname -m)-linux" && \
    chmod 755 /lightpanda

# fx is the agent. den needs a few changes fx does not have yet, so the
# image takes a release of the fork that carries them and checks its digest.
# A Linux fx at build/fx, such as one built from an fx checkout, takes its
# place.
ARG FX_VERSION=v0.0.16-den.2
ARG FX_SHA256_AARCH64=827135f69d5713a9efad6e531d5a706abed73b6487c8c2113e4d0b7f45b0fc95
ARG FX_SHA256_X86_64=c49945e9e5ddfb1ccda8a1de2564db260f14032b93cede2f5dd0218ad678773d
COPY build/ /tmp/build/
RUN if [ -f /tmp/build/fx ]; then \
        cp /tmp/build/fx /fx; \
    else \
        arch=$(uname -m); \
        case "$arch" in \
            aarch64) sum=$FX_SHA256_AARCH64 ;; \
            x86_64) sum=$FX_SHA256_X86_64 ;; \
            *) echo "no fx release for $arch" >&2; exit 1 ;; \
        esac; \
        curl -fsSL -o /fx "https://github.com/unsady/fx/releases/download/$FX_VERSION/fx-linux-$arch" && \
        echo "$sum  /fx" | sha256sum -c -; \
    fi && \
    chmod 755 /fx

FROM debian:trixie-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
        bash ca-certificates curl git jq less opus-tools procps ripgrep sqlite3 tini tzdata xz-utils && \
    rm -rf /var/lib/apt/lists/*

# The agent installs its own tools, such as yt-dlp, in ~/.local/bin. Debian's
# /etc/profile resets PATH in the login shells fx runs commands in, and the
# home volume starts without a ~/.profile to add it back.
RUN printf '%s\n' 'case ":$PATH:" in *":$HOME/.local/bin:"*) ;; *) PATH="$HOME/.local/bin:$PATH" ;; esac' \
        > /etc/profile.d/local-bin.sh

# git reaches GitHub through den, which adds the user's token and asks before
# a push changes a branch the agent did not create.
RUN git config --system url."http://127.0.0.1:4300/".insteadOf https://github.com/ && \
    git config --system --add url."http://127.0.0.1:4300/".insteadOf git@github.com:

RUN useradd --create-home --uid 1000 --shell /bin/bash agent && \
    mkdir -p /var/lib/den /run/den

COPY --from=build /out/den /usr/local/bin/den
COPY --from=whisper /src/build/bin/whisper-cli /usr/local/bin/whisper-cli
COPY --from=downloads /lightpanda /usr/local/bin/lightpanda
COPY --from=downloads /fx /usr/local/bin/fx
# Built-in skills belong to root, so the agent can read them but not change
# them. den links each into the agent's ~/.agents/skills on start, the
# profile skill folder where fx follows links into the folders that
# FX_SKILL_SYMLINK_AUTHORITIES names.
COPY skills /usr/share/den/skills

# fx reaches ChatGPT's Codex through den's model proxy, which holds the
# account's token; fx itself keeps only a stand-in.
ENV FX_TEST_OPENAI_CODEX_RESPONSES_URL=http://127.0.0.1:4100/codex/responses \
    FX_TEST_OPENAI_CODEX_MODELS_URL=http://127.0.0.1:4100/codex/models \
    FX_TEST_CHATGPT_TOKEN_URL=http://127.0.0.1:4100/codex-auth/oauth/token

ENV DEN_STATE=/var/lib/den \
    DEN_SPACE=/home/agent/space \
    DEN_ARCHIVE=/home/agent/archive \
    DEN_INBOX=/home/agent/inbox \
    DEN_SKILLS=/usr/share/den/skills \
    DEN_FX_SKILLS=/home/agent/.agents/skills \
    FX_SKILL_SYMLINK_AUTHORITIES=/usr/share/den/skills \
    DEN_FX_SESSIONS=/home/agent/.fx/sessions/v2 \
    DEN_FX=/usr/local/bin/fx \
    DEN_SOCKET=/run/den/den.sock \
    DEN_AGENT_USER=agent \
    FX_SOUND=0

VOLUME ["/home/agent", "/var/lib/den"]

# The daemon runs as root to keep the secrets from the agent. It runs fx and
# `den init` as the agent user.
ENTRYPOINT ["tini", "--"]
CMD ["den", "serve"]
