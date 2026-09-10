#!/usr/bin/env bash
# Report candidate PR Risk capabilities without changing the current gates.

set -euo pipefail

embedded_cli=false
proxied_cli=false
embedded_storage=false
embedded_conformance=false
server_conformance=false
full=false
reason=""
diff_file=""

cleanup() {
	if [[ -n "$diff_file" ]]; then
		rm -f "$diff_file"
	fi
}
trap cleanup EXIT

set_full() {
	embedded_cli=true
	proxied_cli=true
	embedded_storage=true
	embedded_conformance=true
	server_conformance=true
	full=true
	reason="$1"
}

emit() {
	local lines=(
		"embedded_cli=$embedded_cli"
		"proxied_cli=$proxied_cli"
		"embedded_storage=$embedded_storage"
		"embedded_conformance=$embedded_conformance"
		"server_conformance=$server_conformance"
		"full=$full"
		"reason=$reason"
	)
	printf '%s\n' "${lines[@]}"
	if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
		printf '%s\n' "${lines[@]}" >> "$GITHUB_OUTPUT"
	fi
}

event_name="${GITHUB_EVENT_NAME:-}"
base_sha="${PR_BASE_SHA:-}"
head_sha="${PR_HEAD_SHA:-}"
case "$event_name" in
	pull_request) ;;
	push) set_full "push"; emit; exit 0 ;;
	merge_group) set_full "merge_group"; emit; exit 0 ;;
	*) set_full "unsupported_event"; emit; exit 0 ;;
esac

if [[ -z "$base_sha" || -z "$head_sha" ]]; then
	set_full "missing_bounds"
	emit
	exit 0
fi

diff_file=$(mktemp)
if ! git diff --no-ext-diff --find-renames --name-status -z "$base_sha" "$head_sha" -- > "$diff_file"; then
	set_full "diff_failed"
	emit
	exit 0
fi

saw_path=false
saw_cmd=false
saw_embedded=false
saw_server=false
saw_conformance=false

while :; do
	status=""
	if ! IFS= read -r -d '' status <&3; then
		if [[ -n "$status" ]]; then
			set_full "malformed_record"
		fi
		break
	fi
	if [[ "$status" != "A" && "$status" != "M" ]]; then
		set_full "unsafe_status"
		break
	fi
	path=""
	if ! IFS= read -r -d '' path <&3; then
		set_full "malformed_record"
		break
	fi
	saw_path=true
	if [[ -z "$path" || "$path" == /* || "/$path/" == *"/../"* || "$path" == . || "$path" == .. || "$path" == ./* ]]; then
		set_full "unsafe_path"
		break
	fi
	case "$path" in
		.github/workflows/*|.github/scripts/*)
			set_full "unsafe_control"
			break
			;;
		.buildflags|go.mod|go.sum|Makefile|default.nix|flake.nix|flake.lock|overlay.nix|packages.nix)
			set_full "build_input"
			break
			;;
		cmd/bd/*)
			embedded_cli=true
			proxied_cli=true
			saw_cmd=true
			;;
		internal/storage/embeddeddolt/*)
			embedded_cli=true
			embedded_storage=true
			embedded_conformance=true
			saw_embedded=true
			;;
		internal/storage/dolt/*)
			proxied_cli=true
			server_conformance=true
			saw_server=true
			;;
		backend/conformance/*|test/conformance/*)
			embedded_conformance=true
			server_conformance=true
			saw_conformance=true
			;;
		internal/storage/*|backend/*|schema/*)
			set_full "cross_cutting_storage"
			break
			;;
		docs/*.md|docs/*.mdx|docs/*.png|docs/*.jpg|docs/*.jpeg|docs/*.gif|docs/*.svg|docs/*.webp|docs/*.avif|engdocs/*.md|engdocs/*.mdx|engdocs/*.png|engdocs/*.jpg|engdocs/*.jpeg|engdocs/*.gif|engdocs/*.svg|engdocs/*.webp|engdocs/*.avif|README.md|CHANGELOG.md|ARTICLES.md|BENCHMARKS.md|NEWSLETTER.md)
			;;
		*)
			set_full "unowned_path"
			break
			;;
	esac
done 3< "$diff_file"

if [[ "$full" != true ]]; then
	if [[ "$embedded_cli" == true && "$proxied_cli" == true && "$embedded_storage" == true && "$embedded_conformance" == true && "$server_conformance" == true ]]; then
		full=true
		reason="full"
	elif [[ "$saw_path" != true ]]; then
		reason="empty_diff"
	elif [[ "$saw_cmd" == true && "$saw_embedded" != true && "$saw_server" != true && "$saw_conformance" != true ]]; then
		reason="cmd_bd"
	elif [[ "$saw_embedded" == true && "$saw_cmd" != true && "$saw_server" != true && "$saw_conformance" != true ]]; then
		reason="embedded_storage"
	elif [[ "$saw_server" == true && "$saw_cmd" != true && "$saw_embedded" != true && "$saw_conformance" != true ]]; then
		reason="server_storage"
	elif [[ "$saw_conformance" == true && "$saw_cmd" != true && "$saw_embedded" != true && "$saw_server" != true ]]; then
		reason="shared_conformance"
	elif [[ "$saw_cmd" == false && "$saw_embedded" == false && "$saw_server" == false && "$saw_conformance" == false ]]; then
		reason="docs_only"
	else
		reason="mixed"
	fi
fi

emit
