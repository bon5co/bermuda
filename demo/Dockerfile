# A clean Ubuntu with nothing on it but what bermuda actually needs.
#
# Two jobs: prove the install instructions in the README work on a machine that
# has never seen this project, and produce the screenshots in it. Both matter —
# a screenshot taken on the author's machine proves nothing about the install,
# and an install nobody looks at proves nothing about the board.
FROM ubuntu:24.04

ARG GO_VERSION=1.26.5
ARG HERDR_VERSION=v0.7.5
ARG VHS_VERSION=v0.11.0
ARG TTYD_VERSION=1.7.7

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl git make unzip ffmpeg fontconfig \
    && rm -rf /var/lib/apt/lists/*

# VHS screenshots through a headless Chromium, which it fetches itself and which
# needs these to start at all. Without them it downloads the browser, fails on
# libnss3, and reports only "recording failed".
RUN apt-get update && apt-get install -y --no-install-recommends \
        libnss3 libatk1.0-0t64 libatk-bridge2.0-0t64 libcups2t64 libdrm2 \
        libxkbcommon0 libxcomposite1 libxdamage1 libxfixes3 libxrandr2 \
        libgbm1 libpango-1.0-0 libcairo2 libasound2t64 \
    && rm -rf /var/lib/apt/lists/*

# Go, from go.dev rather than apt: bermuda's go.mod asks for a toolchain newer
# than Ubuntu ships.
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" \
      | tar -C /usr/local -xz
ENV PATH="/usr/local/go/bin:/usr/local/bin:${PATH}"

# Herdr, the terminal multiplexer bermuda runs its agents in.
RUN curl -fsSL -o /usr/local/bin/herdr \
      "https://github.com/ogulcancelik/herdr/releases/download/${HERDR_VERSION}/herdr-linux-x86_64" \
    && chmod +x /usr/local/bin/herdr

# VHS drives a real terminal from a script and writes PNGs, which is the only
# honest way to screenshot a TUI: no cropping, no compositing, no editing.
RUN curl -fsSL "https://github.com/tsl0922/ttyd/releases/download/${TTYD_VERSION}/ttyd.x86_64" \
      -o /usr/local/bin/ttyd \
    && chmod +x /usr/local/bin/ttyd \
    && GOBIN=/usr/local/bin go install "github.com/charmbracelet/vhs@${VHS_VERSION}"

# A font with the box-drawing and block characters the board is made of.
RUN curl -fsSL -o /tmp/jetbrains.zip \
      "https://github.com/ryanoasis/nerd-fonts/releases/download/v3.2.1/JetBrainsMono.zip" \
    && mkdir -p /usr/share/fonts/truetype/jetbrains \
    && cd /usr/share/fonts/truetype/jetbrains \
    && unzip -o -q /tmp/jetbrains.zip 'JetBrainsMonoNerdFont-*.ttf' \
    && rm /tmp/jetbrains.zip \
    && fc-cache -f

WORKDIR /src
COPY . /src
# The version is passed in rather than derived: the build context has no .git,
# so `git describe` inside here would stamp every screenshot "dev".
#
# The path carries /v2. Without it the -X flag names a symbol in a package that
# does not exist, which Go accepts in silence — the build succeeds, the stamp
# lands nowhere, and every screenshot says "dev" anyway, which is the failure
# this ARG exists to prevent.
ARG VERSION=dev
RUN go build -ldflags "-X github.com/bon5co/bermuda/v3/internal/version.Tag=${VERSION}" \
      -o /usr/local/bin/bermuda ./cmd/bermuda

# The demo store is built at run time, not baked in: it carries timestamps, and
# a screenshot showing "3 days ago" for something the image built in March is
# worse than no screenshot.
ENV BERMUDA_STATE_DIR=/home/ubuntu/.bermuda-demo
ENV TERM=xterm-256color

# Not root: the headless Chromium VHS drives refuses to start as root without
# --no-sandbox, which VHS gives no way to pass. Ubuntu's stock `ubuntu` user is
# uid 1000, so files written to a mounted /out belong to the caller too.
RUN chown -R ubuntu:ubuntu /src
USER ubuntu

ENTRYPOINT ["/src/demo/capture.sh"]
