#!/bin/sh

set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
specs_dir="$repo_root/specs"

duplicate_ids=$(
	for spec_dir in "$specs_dir"/[0-9][0-9][0-9]-*; do
		[ -d "$spec_dir" ] || continue
		spec_name=${spec_dir##*/}
		printf '%s\n' "${spec_name%%-*}"
	done | sort | uniq -d
)

if [ -z "$duplicate_ids" ]; then
	printf '%s\n' "spec numbers are unique"
	exit 0
fi

printf '%s\n' "duplicate spec numbers:" >&2
for duplicate_id in $duplicate_ids; do
	for spec_dir in "$specs_dir"/"$duplicate_id"-*; do
		[ -d "$spec_dir" ] || continue
		printf '  %s\n' "${spec_dir#"$repo_root"/}" >&2
	done
done
exit 1
