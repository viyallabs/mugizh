# Deploy tool image (S-094). Pinned versions; built and pushed by the `deployimage` CI job to
# $CI_REGISTRY_IMAGE/deploy:1. Holds OpenTofu, AWS CLI v2, crane (registry copy by digest), jq, curl.
# Local check: docker build -t mugizh-deploy:check tools/deployimage
# Every download is verified before use; a mismatch fails the build (K-SEC-06).

# Debian bookworm 20251117 (more than two weeks old).
ARG BASE=debian:bookworm-20251117-slim

# ---- fetch and verify (this stage is discarded) ----
FROM ${BASE} AS fetch
# apt reads a dated snapshot of the Debian archive (snapshot.debian.org), not the live mirror, so the exact
# package versions below stay installable. Packages are still verified with the Debian archive keys
# (the snapshot's Release files are old, hence Check-Valid-Until=false). Bump SNAPSHOT and the pins together.
ARG SNAPSHOT=20261108T000000Z
RUN rm -f /etc/apt/sources.list.d/debian.sources \
 && printf '%s\n' \
      "deb [signed-by=/usr/share/keyrings/debian-archive-keyring.gpg] http://snapshot.debian.org/archive/debian/${SNAPSHOT}/ bookworm main" \
      "deb [signed-by=/usr/share/keyrings/debian-archive-keyring.gpg] http://snapshot.debian.org/archive/debian/${SNAPSHOT}/ bookworm-updates main" \
      "deb [signed-by=/usr/share/keyrings/debian-archive-keyring.gpg] http://snapshot.debian.org/archive/debian-security/${SNAPSHOT}/ bookworm-security main" \
      > /etc/apt/sources.list \
 && echo 'Acquire::Check-Valid-Until "false";' > /etc/apt/apt.conf.d/99snapshot
# Build-stage tools only; not part of the final image.
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl unzip gnupg \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /dl

# OpenTofu 1.13.9. Checksum copied from the release's tofu_1.13.9_SHA256SUMS (linux_amd64 line).
ARG TOFU_VERSION=1.13.9
ARG TOFU_SHA256=bc44eb6b06446ba83bd4072114891987f590f66cca712737a7f1147c50c539ce
RUN curl -fsSL -o tofu.zip "https://github.com/opentofu/opentofu/releases/download/v${TOFU_VERSION}/tofu_${TOFU_VERSION}_linux_amd64.zip" \
 && echo "${TOFU_SHA256}  tofu.zip" | sha256sum -c - \
 && unzip -q tofu.zip tofu -d /out

# crane v0.22.1 (google/go-containerregistry). Checksum copied from the release's checksums.txt.
ARG CRANE_VERSION=0.22.1
ARG CRANE_SHA256=0ab7a1d6932a213aed964ce97666c3077fe691c8606413674a8b3e0b9ec4cda0
RUN curl -fsSL -o crane.tgz "https://github.com/google/go-containerregistry/releases/download/v${CRANE_VERSION}/go-containerregistry_Linux_x86_64.tar.gz" \
 && echo "${CRANE_SHA256}  crane.tgz" | sha256sum -c - \
 && tar -xzf crane.tgz -C /out crane

# AWS CLI v2.31.0. AWS publishes no checksum for the zip; it publishes a detached GPG signature
# (<zip>.sig) and its public key (copied from the AWS CLI install guide into awscli-public-key.asc).
# We check the key's fingerprint against the documented value FB5D B77F D5C1 18B8 0511 ADA8 A631 0ACC 4672 475C,
# then the signature. [VALIDATE] re-check that fingerprint against the AWS documentation when bumping the version.
ARG AWSCLI_VERSION=2.31.0
ARG AWSCLI_SIGNER_FPR=FB5DB77FD5C118B80511ADA8A6310ACC4672475C
COPY awscli-public-key.asc /dl/awscli-public-key.asc
RUN export GNUPGHOME="$(mktemp -d)" \
 && gpg --batch --import /dl/awscli-public-key.asc \
 && gpg --batch --with-colons --fingerprint | grep -q "^fpr:::::::::${AWSCLI_SIGNER_FPR}:" \
 && curl -fsSL -o awscli.zip "https://awscli.amazonaws.com/awscli-exe-linux-x86_64-${AWSCLI_VERSION}.zip" \
 && curl -fsSL -o awscli.zip.sig "https://awscli.amazonaws.com/awscli-exe-linux-x86_64-${AWSCLI_VERSION}.zip.sig" \
 && gpg --batch --status-fd 1 --verify awscli.zip.sig awscli.zip | grep -q "^\[GNUPG:\] VALIDSIG ${AWSCLI_SIGNER_FPR} " \
 && unzip -q awscli.zip \
 && ./aws/install --install-dir /usr/local/aws-cli --bin-dir /usr/local/aws-bin

# ---- final image ----
FROM ${BASE}
# apt reads a dated snapshot of the Debian archive (snapshot.debian.org), not the live mirror, so the exact
# package versions below stay installable. Packages are still verified with the Debian archive keys
# (the snapshot's Release files are old, hence Check-Valid-Until=false). Bump SNAPSHOT and the pins together.
ARG SNAPSHOT=20261108T000000Z
RUN rm -f /etc/apt/sources.list.d/debian.sources \
 && printf '%s\n' \
      "deb [signed-by=/usr/share/keyrings/debian-archive-keyring.gpg] http://snapshot.debian.org/archive/debian/${SNAPSHOT}/ bookworm main" \
      "deb [signed-by=/usr/share/keyrings/debian-archive-keyring.gpg] http://snapshot.debian.org/archive/debian/${SNAPSHOT}/ bookworm-updates main" \
      "deb [signed-by=/usr/share/keyrings/debian-archive-keyring.gpg] http://snapshot.debian.org/archive/debian-security/${SNAPSHOT}/ bookworm-security main" \
      > /etc/apt/sources.list \
 && echo 'Acquire::Check-Valid-Until "false";' > /etc/apt/apt.conf.d/99snapshot
# Base-image packages are upgraded to the snapshot (security fixes), then the tools are installed at exact versions.
RUN apt-get update \
 && apt-get upgrade -y --no-install-recommends \
 && apt-get install -y --no-install-recommends \
      ca-certificates=20250419~deb12u2 \
      curl=7.88.1-10+deb12u15 \
      jq=1.6-2.1+deb12u3 \
 && rm -rf /var/lib/apt/lists/*
COPY --from=fetch /out/tofu /out/crane /usr/local/bin/
COPY --from=fetch /usr/local/aws-cli /usr/local/aws-cli
RUN ln -s /usr/local/aws-cli/v2/current/bin/aws /usr/local/bin/aws
# Regional STS only: credentials from the global endpoint are not valid in ap-south-2 (infra/README.md).
ENV AWS_STS_REGIONAL_ENDPOINTS=regional AWS_PAGER="" TF_IN_AUTOMATION=1
WORKDIR /src
# Non-root (Trivy AVD-DS-0002). The CI job mounts the checked-out repo itself.
RUN useradd --system --uid 10001 --create-home deploy
USER 10001:10001
