# Sourced by run.sh (and scenarioSets.test.ts): the SCENARIOS aliases.
#   (empty) | all  every scenario: bus + git + chat
#   bus            1-8, 3b, 3t, 7b: the event-bus regression set (no git fixture build)
#   git            9a-9h: the real-git scenarios (build the fixture, delays off)
#   chat           10a-10c, 10ap-10cp: a live reply streaming into a long
#                  conversation, three sizes loaded, unpaced and paced (delays off)
#   none           no scenarios (checks only), passed through
# Aliases mix with ids: SCENARIOS=bus,9a. scenarioSets.test.ts keeps
# bus + git + chat equal to scenarios.ts's SCENARIOS.
PERF_SCENARIOS_BUS=1,2,3,3b,3t,4,5,6,7,7b,8
PERF_SCENARIOS_GIT=9a,9b,9c,9d,9e,9f,9g,9h
PERF_SCENARIOS_CHAT=10a,10b,10c,10ap,10bp,10cp

# resolve_scenarios <SCENARIOS>: prints the comma list with aliases expanded.
resolve_scenarios() {
	_in=${1:-all}
	_out=
	for _s in $(echo "$_in" | tr ',' ' '); do
		case "$_s" in
		all) _s="$PERF_SCENARIOS_BUS,$PERF_SCENARIOS_GIT,$PERF_SCENARIOS_CHAT" ;;
		bus) _s=$PERF_SCENARIOS_BUS ;;
		git) _s=$PERF_SCENARIOS_GIT ;;
		chat) _s=$PERF_SCENARIOS_CHAT ;;
		esac
		_out=${_out:+$_out,}$_s
	done
	echo "$_out"
}
