# The exhaustive census is intentionally tied to this linux/amd64 base. Do not
# replace the digest with an Ubuntu tag: historical Dolt v0.55.4 needs ICU 74,
# and source-built releases record the GCC 13.3.0 ABI in their provenance.
FROM ubuntu@sha256:52df9b1ee71626e0088f7d400d5c6b5f7bb916f8f0c82b474289a4ece6cf3faf

ARG APT_SNAPSHOT=20260720T000000Z
ARG GO_VERSION=1.26.5
ARG GO_SHA256=5c2c3b16caefa1d968a94c1daca04a7ca301a496d9b086e17ad77bb81393f053
ARG DOLT_VERSION=v2.1.8
ARG DOLT_SHA256=f66318f08ed66e409fc39363ae0fff8ce6fbf6dba9f5bac632b91527b9632a74

# The base image deliberately omits CA certificates, and the Ubuntu snapshot
# service is HTTPS-only (a plain-http request 301-redirects to https), so apt
# needs a trust store before it can read the pinned snapshot. Bootstrap ONLY
# that trust material -- and deliberately UNPINNED -- from the base image's
# default repositories: an unpinned install always resolves to a currently
# available version, so a scheduled rebuild never breaks when a specific version
# is pruned or superseded in those mutable live repos. ca-certificates/openssl
# are not part of the reproducibility-critical toolchain (gcc/g++/libicu/libc/
# dolt, pinned below); they only establish TLS to fetch the snapshot and the
# Go/Dolt tarballs. Every EXACT-pinned package is then installed from the
# immutable snapshot, so no pinned version is ever sourced from a mutable repo.
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      ca-certificates \
      openssl \
 && rm -rf /var/lib/apt/lists/* \
 && printf '%s\n' \
      'Types: deb' \
      "URIs: https://snapshot.ubuntu.com/ubuntu/${APT_SNAPSHOT}/" \
      'Suites: noble noble-updates noble-security' \
      'Components: main universe restricted multiverse' \
      'Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg' \
      > /etc/apt/sources.list.d/ubuntu.sources \
 && apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      curl=8.5.0-2ubuntu10.11 \
      gcc=4:13.2.0-7ubuntu1 \
      g++=4:13.2.0-7ubuntu1 \
      gcc-13=13.3.0-6ubuntu2~24.04.1 \
      g++-13=13.3.0-6ubuntu2~24.04.1 \
      git=1:2.43.0-1ubuntu7.3 \
      libicu74=74.2-1ubuntu3.1 \
      lsof=4.95.0-1build3 \
 && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL "https://dl.google.com/go/go${GO_VERSION}.linux-amd64.tar.gz" -o /tmp/go.tgz \
 && echo "${GO_SHA256}  /tmp/go.tgz" | sha256sum -c - \
 && tar -C /usr/local -xzf /tmp/go.tgz \
 && rm /tmp/go.tgz \
 && /usr/local/go/bin/go version \
 && test "$(gcc --version | head -n 1)" = 'gcc (Ubuntu 13.3.0-6ubuntu2~24.04.1) 13.3.0'

RUN curl -fsSL "https://github.com/dolthub/dolt/releases/download/${DOLT_VERSION}/dolt-linux-amd64.tar.gz" -o /tmp/dolt.tgz \
 && echo "${DOLT_SHA256}  /tmp/dolt.tgz" | sha256sum -c - \
 && mkdir -p /tmp/dolt /opt/dolt/bin \
 && tar -xzf /tmp/dolt.tgz -C /tmp/dolt \
 && install -m 0755 /tmp/dolt/dolt-linux-amd64/bin/dolt /opt/dolt/bin/dolt \
 && rm -rf /tmp/dolt /tmp/dolt.tgz \
 && /opt/dolt/bin/dolt version \
 && test "$(dpkg-query -W -f='${Version}' libc6:amd64)" = '2.39-0ubuntu8.7' \
 && test "$(dpkg-query -W -f='${Version}' libicu74)" = '74.2-1ubuntu3.1'

ENV PATH=/usr/local/go/bin:/opt/dolt/bin:${PATH} \
    GOTOOLCHAIN=local \
    DOLT_BIN=/opt/dolt/bin/dolt \
    GIT_CONFIG_NOSYSTEM=1 \
    GODEBUG=goindex=0

WORKDIR /workspace
