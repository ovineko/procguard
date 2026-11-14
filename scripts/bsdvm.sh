#!/bin/sh
set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
vm_dir=${PROCGUARD_VM_DIR:-"$HOME/.cache/procguard-vm"}
release=14.5-RELEASE
image="FreeBSD-$release-amd64-BASIC-CLOUDINIT-ufs.qcow2.xz"
url="https://download.freebsd.org/releases/VM-IMAGES/$release/amd64/Latest"
container=procguard-freebsd
platform=linux/amd64
alpine=alpine:3.23@sha256:469c9ea410b9215eaee7976ba6c0639039b444be8e5d638f7abbada189971eb2

usage() {
  echo 'Usage: scripts/bsdvm.sh up|test|down' >&2
  exit 2
}
[ "$#" -eq 1 ] || usage
case "$1" in up | test | down) ;; *) usage ;; esac
mkdir -p "$vm_dir"
vm_dir=$(CDPATH='' cd -- "$vm_dir" && pwd)
mkdir "$vm_dir/lock" || {
  echo 'Another VM operation holds the cache lock.' >&2
  exit 1
}
trap 'rmdir "$vm_dir/lock"' 0
trap 'exit 1' HUP INT TERM

vm_ssh() {
  ssh -i "$vm_dir/id_ed25519" -p 2222 -o BatchMode=yes \
    -o IdentitiesOnly=yes -o ConnectTimeout=5 -o StrictHostKeyChecking=accept-new \
    -o "UserKnownHostsFile=$vm_dir/known_hosts" freebsd@127.0.0.1 "$@"
}

download() {
  if [ ! -f "$vm_dir/$1" ]; then
    curl --fail --location --retry 3 --output "$vm_dir/$1.part" "$url/$1"
    mv "$vm_dir/$1.part" "$vm_dir/$1"
  fi
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
  download CHECKSUM.SHA256
  download "$image"
  digest=$(awk -v name="$image" '
    $1 == "SHA256" && $2 == "(" name ")" && $3 == "=" { print $4 }
    $2 == name || $2 == "*" name { print $1 }
  ' "$vm_dir/CHECKSUM.SHA256")
  [ "${#digest}" -eq 64 ] || {
    echo 'Missing or ambiguous image checksum.' >&2
    exit 1
  }
  case "$digest" in *[!0-9a-fA-F]*)
    echo 'Invalid SHA256 checksum.' >&2
    exit 1
    ;;
  esac
  (cd "$vm_dir" && printf '%s  %s\n' "$digest" "$image" | sha256sum -c -)
  if [ ! -f "$vm_dir/base.qcow2" ]; then
    xz -dc "$vm_dir/$image" > "$vm_dir/base.qcow2.part"
    mv "$vm_dir/base.qcow2.part" "$vm_dir/base.qcow2"
  fi
  if [ ! -f "$vm_dir/id_ed25519" ]; then
    ssh-keygen -q -t ed25519 -N '' -f "$vm_dir/id_ed25519"
  fi
  mkdir -p "$vm_dir/seed"
  public_key=$(cat "$vm_dir/id_ed25519.pub")
  cat > "$vm_dir/seed/user-data" << SEED
#cloud-config
users:
  - name: freebsd
    groups: wheel
    ssh_authorized_keys:
      - $public_key
SEED
  # Every up boots a fresh overlay over the verified base image: cloud-init only
  # applies users on first boot, and a reused disk would keep stale state.
  if docker container inspect "$container" > /dev/null 2>&1; then
    docker rm -f "$container" > /dev/null
  fi
  printf 'instance-id: procguard-freebsd\nlocal-hostname: procguard-freebsd\n' > "$vm_dir/seed/meta-data"
  docker run --rm --platform "$platform" \
    --mount "type=bind,src=$vm_dir,dst=/vm" "$alpine" sh -ec '
      for attempt in 1 2 3 4 5; do apk add --no-cache xorriso qemu-img && break; [ "$attempt" -lt 5 ] || exit 1; sleep 3; done
      xorriso -as mkisofs -output /vm/seed.iso -volid cidata -joliet -rock /vm/seed
      rm -f /vm/disk.qcow2
      qemu-img create -f qcow2 -F qcow2 -b /vm/base.qcow2 /vm/disk.qcow2
    '
  rm -f "$vm_dir/known_hosts"
  docker run -d --name "$container" --platform "$platform" \
    --device /dev/kvm -p 127.0.0.1:2222:2222 \
    --mount "type=bind,src=$vm_dir,dst=/vm" "$alpine" sh -ec '
        for attempt in 1 2 3 4 5; do apk add --no-cache qemu-system-x86_64 qemu-img && break; [ "$attempt" -lt 5 ] || exit 1; sleep 3; done
        exec qemu-system-x86_64 -enable-kvm -cpu host -smp 4 -m 4096 -nographic \
          -drive file=/vm/disk.qcow2,format=qcow2,if=virtio \
          -cdrom /vm/seed.iso -netdev user,id=net0,hostfwd=tcp::2222-:22 \
          -device virtio-net-pci,netdev=net0
      '
  attempt=0
  until vm_ssh true 2> /dev/null; do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 120 ]; then
      docker logs --tail 40 "$container" >&2
      echo 'SSH readiness deadline exceeded.' >&2
      exit 1
    fi
    sleep 2
  done
  echo 'FreeBSD VM is ready on localhost:2222.'
}

test_vm() {
  cd "$root"
  CGO_ENABLED=0 GOOS=freebsd GOARCH=amd64 go test -c -o "$vm_dir/procguard.test" .
  CGO_ENABLED=0 GOOS=freebsd GOARCH=amd64 go test -c -tags e2e -o "$vm_dir/e2e.test" ./e2e
  CGO_ENABLED=0 GOOS=freebsd GOARCH=amd64 go build -tags e2e -o "$vm_dir/e2e-fixture" ./e2e/fixture
  tar -czf "$vm_dir/source.tar.gz" go.mod go.sum ./*.go internal e2e examples testdata docs/guides/platform-guarantees.md
  vm_ssh 'su -m root -c "env ASSUME_ALWAYS_YES=yes pkg bootstrap -f && pkg install -y go"'
  vm_ssh 'cat > source.tar.gz' < "$vm_dir/source.tar.gz"
  vm_ssh 'cat > procguard.test && chmod +x procguard.test' < "$vm_dir/procguard.test"
  vm_ssh 'cat > e2e.test && chmod +x e2e.test' < "$vm_dir/e2e.test"
  vm_ssh 'cat > e2e-fixture && chmod +x e2e-fixture' < "$vm_dir/e2e-fixture"
  vm_ssh sh -s << 'RUN'
set -eu
export PATH="/usr/local/bin:$PATH"
work=$(mktemp -d "$HOME/procguard.XXXXXX")
trap 'rm -rf "$work" "$HOME/source.tar.gz" "$HOME/procguard.test" "$HOME/e2e.test" "$HOME/e2e-fixture"' 0
trap 'exit 1' HUP INT TERM
tar -xzf "$HOME/source.tar.gz" -C "$work"
cd "$work"
generator=$(mktemp)
cc -Wall -Wextra -Werror internal/bsdabi/generate.c -o "$generator"
"$generator" freebsd amd64 > generated.go
cmp generated.go zprocabi_freebsd_amd64.go
rm -f "$generator" generated.go
"$HOME/procguard.test" -test.v -test.count=1
go test -count=1 ./...
go test -race -count=3 ./...
PROCGUARD_E2E_FIXTURE="$HOME/e2e-fixture" PROCGUARD_E2E_ROOT="$work" "$HOME/e2e.test" -test.v -test.count=1
PROCGUARD_MODEL_RUNS=1000 go test -race -run Model ./...
RUN
}

case "$1" in
  up) up ;;
  test) test_vm ;;
  down)
    if docker container inspect "$container" > /dev/null 2>&1; then
      docker stop --time 30 "$container" > /dev/null
      docker rm "$container" > /dev/null
    fi
    ;;
esac
