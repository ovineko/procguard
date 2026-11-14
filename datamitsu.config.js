/// <reference path=".datamitsu/datamitsu.config.d.ts" />

const golangciLint = {
  binaries: {
    darwin: {
      amd64: {
        unknown: {
          binaryPath: "golangci-lint-2.15.0/golangci-lint",
          contentType: "tar.gz",
          hash: "sha256:767b899a28285e88ded2823d5dcc4bc5ef681173eb2d80f029a7b17ee4ece3b8",
          url: "https://github.com/golangci/golangci-lint/releases/download/v2.15.0/golangci-lint-2.15.0-darwin-amd64.tar.gz",
        },
      },
      arm64: {
        unknown: {
          binaryPath: "golangci-lint-2.15.0/golangci-lint",
          contentType: "tar.gz",
          hash: "sha256:d1e409481a8ca846c4bc97e781eb5cb1bd5c23353a0b3977a1cbda1b1b6447d5",
          url: "https://github.com/golangci/golangci-lint/releases/download/v2.15.0/golangci-lint-2.15.0-darwin-arm64.tar.gz",
        },
      },
    },
    freebsd: {
      amd64: {
        unknown: {
          binaryPath: "golangci-lint-2.15.0/golangci-lint",
          contentType: "tar.gz",
          hash: "sha256:4af60f7a5f057a0996de9642e9206db4e06666c5e191e53af7c360c26e60f1ef",
          url: "https://github.com/golangci/golangci-lint/releases/download/v2.15.0/golangci-lint-2.15.0-freebsd-amd64.tar.gz",
        },
      },
      arm64: {
        unknown: {
          binaryPath: "golangci-lint-2.15.0/golangci-lint",
          contentType: "tar.gz",
          hash: "sha256:7780ed9c5ec5b1f184b3d7ba700695312afd773b7a9a946369ffea2169030a12",
          url: "https://github.com/golangci/golangci-lint/releases/download/v2.15.0/golangci-lint-2.15.0-freebsd-arm64.tar.gz",
        },
      },
    },
    linux: {
      amd64: {
        glibc: {
          binaryPath: "golangci-lint-2.15.0/golangci-lint",
          contentType: "tar.gz",
          hash: "sha256:9b7fe6a6bf6334b98decf8d86b11415ea7889afdabdd1959fdce23af2a9ebcea",
          url: "https://github.com/golangci/golangci-lint/releases/download/v2.15.0/golangci-lint-2.15.0-linux-amd64.tar.gz",
        },
      },
      arm64: {
        glibc: {
          binaryPath: "golangci-lint-2.15.0/golangci-lint",
          contentType: "tar.gz",
          hash: "sha256:b4d8f17068b35ee6060cc2f69a16fb48740d706fbba81661c318649c114a6998",
          url: "https://github.com/golangci/golangci-lint/releases/download/v2.15.0/golangci-lint-2.15.0-linux-arm64.tar.gz",
        },
      },
    },
    windows: {
      amd64: {
        unknown: {
          binaryPath: "golangci-lint-2.15.0/golangci-lint.exe",
          contentType: "zip",
          hash: "sha256:cd7ea6063c95de245e17e5246e42bdc14bb221792c429dbd239ab3a6ade6ca6a",
          url: "https://github.com/golangci/golangci-lint/releases/download/v2.15.0/golangci-lint-2.15.0-windows-amd64.zip",
        },
      },
      arm64: {
        unknown: {
          binaryPath: "golangci-lint-2.15.0/golangci-lint.exe",
          contentType: "zip",
          hash: "sha256:4fb8988abf186158e30af1b8a0fb3671cf2cb02bc91d7e6ce6996f4c1e922e9a",
          url: "https://github.com/golangci/golangci-lint/releases/download/v2.15.0/golangci-lint-2.15.0-windows-arm64.zip",
        },
      },
    },
  },
  version: "v2.15.0",
};

function getConfig(input) {
  return {
    ...input,
    apps: {
      ...input.apps,
      "golangci-lint": {
        dependsOn: ["procguard-golangci-lint"],
        description: "Go analysis with a matching SDK for embedded ruleguard rules",
        runtimeEnv: {
          GOTOOLCHAIN: "go1.27.0",
          PROCGUARD_LINT_BIN: "${APP_BIN:procguard-golangci-lint}",
        },
        shell: {
          args: [
            "-c",
            'set -eu; unset GOROOT; GOROOT=$(go env GOROOT); export GOROOT; exec "$PROCGUARD_LINT_BIN" "$@"',
            "golangci-lint",
          ],
          name: "sh",
        },
      },
      "procguard-golangci-lint": {
        ...input.apps["golangci-lint"],
        binary: golangciLint,
      },
    },
    tools: {
      ...input.tools,
      "golangci-lint": {
        ...input.tools["golangci-lint"],
        operations: Object.fromEntries(
          Object.entries(input.tools["golangci-lint"].operations).map(([name, operation]) => [
            name,
            { ...operation, args: [...operation.args, "--build-tags=e2e,e2e_docker"] },
          ]),
        ),
      },
    },
  };
}

globalThis.getConfig = getConfig;
globalThis.getMinVersion = () => "0.5.1";
