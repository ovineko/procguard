#!/bin/sh
set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
vm_dir=${PROCGUARD_VM_DIR:-"$HOME/.cache/procguard-vm/dragonfly"}
image=dragonflybsd-6.4.2-x86-64.qcow2
url=https://github.com/cross-platform-actions/dragonflybsd-builder/releases/download/v0.0.2/$image
digest=7d8850a664cd3a09fdae90b0e16897dda00bbf434694e2ebde10a82c8b36a305
container=procguard-dragonfly
port=2235
platform=linux/amd64
alpine=alpine:3.23@sha256:469c9ea410b9215eaee7976ba6c0639039b444be8e5d638f7abbada189971eb2

usage() {
  echo 'Usage: scripts/dragonflyvm.sh up|test|down' >&2
  exit 2
}
[ "$#" -eq 1 ] || usage
case "$1" in up | test | down) ;; *) usage ;; esac
mkdir -p "$vm_dir"
vm_dir=$(CDPATH='' cd -- "$vm_dir" && pwd)
mkdir "$vm_dir/lock" || {
  echo 'Another DragonFly VM operation holds the cache lock.' >&2
  exit 1
}
trap 'rmdir "$vm_dir/lock"' 0
trap 'exit 1' HUP INT TERM

vm_ssh() {
  docker exec -i "$container" sshpass -p runner ssh -p "$port" -o PreferredAuthentications=password -o PubkeyAuthentication=no \
    -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new \
    -o "UserKnownHostsFile=/vm/known_hosts" root@127.0.0.1 "$@"
}

vm_scp_to() {
  docker exec "$container" sshpass -p runner scp -P "$port" -o PreferredAuthentications=password -o PubkeyAuthentication=no \
    -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new \
    -o "UserKnownHostsFile=/vm/known_hosts" "/vm/artifacts/$1" "root@127.0.0.1:$2"
}

vm_scp_from() {
  docker exec "$container" sshpass -p runner scp -P "$port" -o PreferredAuthentications=password -o PubkeyAuthentication=no \
    -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new \
    -o "UserKnownHostsFile=/vm/known_hosts" "root@127.0.0.1:/tmp/procguard/$1" "/vm/artifacts/$1"
}

up() {
  case $(uname -m) in x86_64 | amd64) ;; *)
    echo 'An amd64 KVM host is required.' >&2
    exit 1
    ;;
  esac
  [ -c /dev/kvm ] || {
    echo '/dev/kvm is required.' >&2
    exit 1
  }
  if docker container inspect "$container" > /dev/null 2>&1; then
    echo "Container $container already exists; use test or down." >&2
    exit 1
  fi
  if [ ! -f "$vm_dir/$image" ]; then
    curl --fail --location --retry 3 --output "$vm_dir/$image.part" "$url"
    mv "$vm_dir/$image.part" "$vm_dir/$image"
  fi
  printf '%s  %s\n' "$digest" "$vm_dir/$image" | sha256sum -c -
  rm -f "$vm_dir/disk.qcow2"
  docker run --rm --platform "$platform" \
    --mount "type=bind,src=$vm_dir,dst=/vm" "$alpine" sh -ec '
      apk add --no-cache qemu-img
      qemu-img create -f qcow2 -F qcow2 -b "/vm/'"$image"'" /vm/disk.qcow2
    '
  rm -f "$vm_dir/known_hosts" "$vm_dir/serial.log"
  docker run -d --name "$container" --platform "$platform" \
    --device /dev/kvm -p "127.0.0.1:$port:$port" \
    --mount "type=bind,src=$vm_dir,dst=/vm" "$alpine" sh -ec '
      apk add --no-cache qemu-system-x86_64 openssh-client sshpass
      exec qemu-system-x86_64 -enable-kvm -machine type=q35,accel=kvm -cpu host \
        -smp 2 -m 4096 -display none -monitor none -serial file:/vm/serial.log \
        -boot strict=off -device virtio-scsi-pci \
        -device scsi-hd,drive=drive0,bootindex=0 \
        -drive if=none,file=/vm/disk.qcow2,id=drive0,cache=writeback,discard=ignore,format=qcow2 \
        -netdev user,id=user.0,hostfwd=tcp::2235-:22,ipv6=off \
        -device virtio-net,netdev=user.0,addr=0x03
    '
  attempt=0
  until vm_ssh true 2> /dev/null; do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 120 ]; then
      cat "$vm_dir/serial.log" >&2 || true
      echo 'DragonFly SSH readiness deadline exceeded.' >&2
      exit 1
    fi
    sleep 2
  done
  vm_ssh 'uname -a'
}

test_vm() {
  cd "$root"
  artifact_dir="$vm_dir/artifacts"
  mkdir -p "$artifact_dir/docs/guides" "$artifact_dir/internal/bsdabi"
  CGO_ENABLED=0 GOOS=dragonfly GOARCH=amd64 go test -c -o "$artifact_dir/procguard.test" .
  CGO_ENABLED=0 GOOS=dragonfly GOARCH=amd64 go test -c -tags e2e -o "$artifact_dir/e2e.test" ./e2e
  CGO_ENABLED=0 GOOS=dragonfly GOARCH=amd64 go build -tags e2e -o "$artifact_dir/e2e-fixture" ./e2e/fixture
  cp internal/bsdabi/generate.c "$artifact_dir/internal/bsdabi/generate.c"
  cp zprocabi_dragonfly_amd64.go "$artifact_dir/zprocabi_dragonfly_amd64.go"
  cp docs/guides/platform-guarantees.md "$artifact_dir/docs/guides/platform-guarantees.md"
  vm_ssh 'mkdir -p /tmp/procguard/docs/guides /tmp/procguard/internal/bsdabi'
  vm_scp_to procguard.test /tmp/procguard/procguard.test
  vm_scp_to e2e.test /tmp/procguard/e2e.test
  vm_scp_to e2e-fixture /tmp/procguard/e2e-fixture
  vm_scp_to internal/bsdabi/generate.c /tmp/procguard/internal/bsdabi/generate.c
  vm_scp_to zprocabi_dragonfly_amd64.go /tmp/procguard/zprocabi_dragonfly_amd64.go
  vm_scp_to docs/guides/platform-guarantees.md /tmp/procguard/docs/guides/platform-guarantees.md
  mkdir -p "$artifact_dir/logs"
  status=0
  if vm_ssh sh -s << 'RUN'
set -eu
cd /tmp/procguard
uname -a
chmod +x procguard.test e2e.test e2e-fixture
tmp=$(mktemp -d)
cc -Wall -Wextra -Werror internal/bsdabi/generate.c -o "$tmp/generate"
"$tmp/generate" dragonfly amd64 > "$tmp/generated.go"
cmp "$tmp/generated.go" zprocabi_dragonfly_amd64.go
rm -rf "$tmp"
result=0
./procguard.test -test.v -test.count=1 > root.log 2>&1 || result=$?
e2e_result=0
PROCGUARD_E2E_FIXTURE=/tmp/procguard/e2e-fixture PROCGUARD_E2E_ROOT=/tmp/procguard ./e2e.test -test.v -test.count=1 > e2e.log 2>&1 || e2e_result=$?
echo "root_status=$result e2e_status=$e2e_result"
[ "$result" -eq 0 ] && [ "$e2e_result" -eq 0 ]
RUN
  then
    status=0
  else
    status=$?
  fi
  vm_scp_from root.log
  vm_scp_from e2e.log
  if [ "$status" -ne 0 ]; then
    echo "DragonFly native tests failed (status $status). Logs: $artifact_dir/root.log, $artifact_dir/e2e.log" >&2
    exit "$status"
  fi
  cp "$artifact_dir/root.log" "$artifact_dir/logs/root.log"
  cp "$artifact_dir/e2e.log" "$artifact_dir/logs/e2e.log"
  echo "DragonFly native root/E2E tests passed; logs: $artifact_dir/logs/"
}

down() {
  if docker container inspect "$container" > /dev/null 2>&1; then
    docker stop --time 30 "$container" > /dev/null
    docker rm "$container" > /dev/null
  fi
}

case "$1" in
  up) up ;;
  test) test_vm ;;
  down) down ;;
esac
