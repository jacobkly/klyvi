#!/bin/sh
# Writes the license and notice files of every Go module compiled into the
# image to one file, so the image carries the notices its licenses ask for.
# Reads "path version folder" lines on stdin, as the Dockerfile's go list
# prints them, and writes to the file named by the first argument:
#
#   go list -deps -f '...' ./cmd/api | sh deploy/licenses.sh /out/licenses/go-modules.txt

set -eu

out=${1:?usage: licenses.sh <output file>}
rule=$(printf '%72s' '' | tr ' ' =)
: >"$out"

sort -u | while read -r path version dir; do
  [ -n "$path" ] || continue
  if [ ! -d "$dir" ]; then
    echo "licenses.sh: no folder for $path $version: '$dir'" >&2
    exit 1
  fi
  {
    printf '%s\n%s %s\n%s\n\n' "$rule" "$path" "$version" "$rule"
    found=0
    for f in "$dir"/*; do
      [ -f "$f" ] || continue
      case $(basename "$f") in
        [Ll][Ii][Cc][Ee][Nn][CcSs][Ee]* | [Cc][Oo][Pp][Yy][Ii][Nn][Gg]* | [Nn][Oo][Tt][Ii][Cc][Ee]*) ;;
        *) continue ;;
      esac
      cat "$f"
      printf '\n'
      found=1
    done
    [ "$found" -eq 1 ] || printf 'No license file in the module.\n'
    printf '\n'
  } >>"$out"
done
