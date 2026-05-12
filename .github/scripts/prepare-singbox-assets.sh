#!/usr/bin/env bash
set -euo pipefail

version="${1:?sing-box version is required}"
goos="${2:?GOOS is required}"
goarch="${3:?GOARCH is required}"
asset_root="${4:-component/subcore/singbox/assets}"

version="${version#v}"
tag="v${version}"

case "${goos}/${goarch}" in
  linux/amd64)
    archive="sing-box-${version}-linux-amd64.tar.gz"
    binary="sing-box"
    cronet="libcronet.so"
    ;;
  linux/arm64)
    archive="sing-box-${version}-linux-arm64.tar.gz"
    binary="sing-box"
    cronet="libcronet.so"
    ;;
  windows/amd64)
    archive="sing-box-${version}-windows-amd64.zip"
    binary="sing-box.exe"
    cronet="libcronet.dll"
    ;;
  windows/arm64)
    archive="sing-box-${version}-windows-arm64.zip"
    binary="sing-box.exe"
    cronet="libcronet.dll"
    ;;
  *)
    echo "unsupported sing-box subcore platform: ${goos}/${goarch}" >&2
    exit 1
    ;;
esac

url="https://github.com/SagerNet/sing-box/releases/download/${tag}/${archive}"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

echo "Downloading ${url}"
curl -fL --retry 3 --retry-delay 2 -o "${tmp_dir}/${archive}" "${url}"

mkdir -p "${tmp_dir}/extract"
case "${archive}" in
  *.zip)
    unzip -q "${tmp_dir}/${archive}" -d "${tmp_dir}/extract"
    ;;
  *.tar.gz)
    tar -xzf "${tmp_dir}/${archive}" -C "${tmp_dir}/extract"
    ;;
esac

src_dir="$(find "${tmp_dir}/extract" -mindepth 1 -maxdepth 1 -type d | head -n 1)"
if [[ -z "${src_dir}" ]]; then
  echo "sing-box archive has no top-level directory" >&2
  exit 1
fi

dest_dir="${asset_root}/${goos}_${goarch}"
rm -rf "${dest_dir}"
mkdir -p "${dest_dir}"

cp "${src_dir}/${binary}" "${dest_dir}/${binary}"
cp "${src_dir}/${cronet}" "${dest_dir}/${cronet}"

if [[ "${goos}" != "windows" ]]; then
  chmod +x "${dest_dir}/${binary}"
fi

echo "Prepared sing-box assets:"
find "${dest_dir}" -maxdepth 1 -type f -printf "  %f\n" | sort
