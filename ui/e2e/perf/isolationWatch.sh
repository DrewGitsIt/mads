#!/bin/sh
# Isolation watch for a realism run (docs/PERF-HARNESS-PLAN.md, phase 4):
# samples, once a second until <stop-file> exists, every process the run's
# stacks own (the pid registry and all their descendants) and records:
#   - any git, claude, codex, hermes, goose or kiro process among them;
#   - any open file outside the run's temp tree and system locations;
#   - any TCP connection to a non-loopback host.
# Writes only counts and process names (never paths: a real path is real
# data) to <out>. Usage: isolationWatch.sh <pids-dir> <run-tmp> <stop-file> <out>
PIDS=$1
TMP=$2
STOP=$3
OUT=$4
samples=0
agents=0
agent_names=""
files=0
nets=0
seen=""
descend() {
	echo "$1"
	for k in $(pgrep -P "$1" 2>/dev/null); do descend "$k"; done
}
while [ ! -f "$STOP" ]; do
	roots=$(cat "$PIDS"/*.txt 2>/dev/null | awk -F'\t' '$1=="pid" && ($4=="data" || $4=="router" || $4=="proxy" || $4=="router-child") {print $2}')
	all=""
	for r in $roots; do all="$all $(descend "$r")"; done
	all=$(echo $all | tr ' ' '\n' | sort -u | tr '\n' ' ')
	if [ -n "$(echo $all | tr -d ' ')" ]; then
		samples=$((samples + 1))
		list=$(echo $all | tr ' ' ',' | sed 's/,$//')
		for c in $(ps -o comm= -p "$list" 2>/dev/null | xargs -n1 basename 2>/dev/null); do
			case "$c" in git | git-* | claude | codex | hermes | goose | kiro-cli)
				agents=$((agents + 1))
				case " $agent_names " in *" $c "*) ;; *) agent_names="$agent_names $c" ;; esac
				;;
			esac
		done
		# Open files outside the run's temp tree and system paths.
		n=$(lsof -nP -p "$list" -Fn 2>/dev/null | sed -n 's/^n//p' | grep '^/' |
			grep -v -e "^$TMP" -e "^/private$TMP" -e '^/System/' -e '^/usr/' -e '^/Library/' -e '^/dev/' \
				-e '^/private/var/db/' -e '^/opt/homebrew/' -e '^/Applications/' -e '^/private/var/folders/.*/C/' \
				-e '^/tmp/mads-' -e '^/private/tmp/mads-' -e '^/$' | sort -u | wc -l | tr -d ' ')
		files=$((files + n))
		m=$(lsof -nP -a -iTCP -p "$list" -Fn 2>/dev/null | sed -n 's/^n//p' | grep -- '->' |
			grep -v -e '->127\.0\.0\.1:' -e '->\[::1\]:' -e '->localhost:' | wc -l | tr -d ' ')
		nets=$((nets + m))
	fi
	sleep 1
done
printf '{"samples":%d,"agent_or_git_process_sightings":%d,"agent_or_git_names":"%s","files_outside_run_or_system":%d,"non_loopback_connections":%d}\n' \
	"$samples" "$agents" "$(echo $agent_names)" "$files" "$nets" >"$OUT"
