#!/usr/bin/env sh
set -eu

root=${1:?PostgreSQL package root is required}
platform=${2:?Platform is required}

case "$platform" in
  darwin-*) ;;
  *) exit 0 ;;
esac

lib_dir="$root/lib"
if [ ! -d "$lib_dir" ]; then
  echo "PostgreSQL package is missing lib directory" >&2
  exit 1
fi

for library in "$lib_dir"/lib*.dylib; do
  [ -f "$library" ] || continue
  [ ! -L "$library" ] || continue
  filename=$(basename "$library")
  stem=${filename%.dylib}
  unversioned=$(printf '%s\n' "$stem" | sed -E 's/(\.[0-9]+)+$//').dylib
  base=${unversioned%.dylib}
  version=${stem#"$base".}
  major="$base.${version%%.*}.dylib"
  for alias in "$major" "$unversioned"; do
    if [ "$alias" != "$filename" ] && [ ! -e "$lib_dir/$alias" ]; then
      ln -s "$filename" "$lib_dir/$alias"
    fi
  done
done
