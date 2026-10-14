package netdiag

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// routeCommand describes one platform's traceroute program.
type routeCommand struct {
	program string
	// method names the program in the result so a reader knows which
	// implementation produced the hops.
	method string
	// args builds the argument list. It is a function rather than a slice
	// because the caller's TTL ceiling and per probe wait have to reach the
	// program; a fixed list would silently measure something other than what
	// was asked for.
	args func(target string, maxHops int, timeout time.Duration) []string
}

// platformTraceroute is the production RouteRunner.
type platformTraceroute struct{}

// platformRouteRunner returns the RouteRunner for this platform.
func platformRouteRunner() RouteRunner { return platformTraceroute{} }

// platformRouteMethod names the traceroute program, or reports that none is
// available.
func platformRouteMethod() string {
	if command, ok := platformTraceCommand(); ok {
		return command.method
	}
	return "unavailable"
}

// Trace runs the platform traceroute and parses its output.
func (platformTraceroute) Trace(
	ctx context.Context, target string, maxHops int, timeout time.Duration, resolve bool,
) (*RouteResult, error) {
	if resolve {
		// Name resolution is refused rather than silently ignored: a resolved
		// name is not evidence about the hop, and resolving every hop would put
		// reverse DNS latency into a latency measurement.
		return nil, fmt.Errorf(
			"%w: hop name resolution is not performed, a resolved name is not evidence about a hop",
			ErrInvalidOptions)
	}
	command, ok := platformTraceCommand()
	if !ok {
		return nil, fmt.Errorf("%w: no traceroute program is configured for this platform", ErrToolUnavailable)
	}
	if maxHops <= 0 {
		maxHops = DefaultMaxHops
	}
	if timeout <= 0 {
		timeout = DefaultTraceWait
	}

	// The run is bounded as a whole so a black holed route cannot hang the
	// caller, on top of each probe's own wait.
	budget := time.Duration(maxHops)*timeout + 10*time.Second
	runCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	arguments := command.args(hostOf(target), maxHops, timeout)
	out, err := exec.CommandContext(runCtx, command.program, arguments...).CombinedOutput()
	route := parseTraceOutput(string(out), maxHops)
	route.Method = command.method
	switch {
	case err != nil && len(route.Hops) == 0:
		return route, fmt.Errorf("%w: %s reported nothing usable: %v", ErrToolUnavailable, command.program, err)
	case err != nil:
		// A partial trace is still real evidence. The failure is recorded as a
		// note instead of replacing the hops that did come back.
		route.Notes = append(route.Notes,
			fmt.Sprintf("the traceroute program exited with an error; the hops below are only the ones it managed to report: %v", err))
	}
	return route, nil
}

// traceRTT matches a round trip as printed by tracert and traceroute. Unix
// traceroute writes "1.234 ms", Windows tracert writes a bare "1" or "12"
// where the unit is established by the header.
var (
	traceRTTWithUnit = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*ms$`)
	traceRTTBare     = regexp.MustCompile(`^(\d+)$`)
	traceAddress     = regexp.MustCompile(`(\d{1,3}(?:\.\d{1,3}){3})`)
)

// parseTraceOutput turns platform traceroute output into a RouteResult.
//
// The parser is conservative about what it claims:
//
//   - A line of stars is a timeout. The hop is kept so the hop count stays
//     honest, and it is never presented as that hop being down: routers
//     routinely rate limit or deprioritise TTL expired messages.
//   - Every responding address on a line is recorded, not only the first one.
//   - A line that matches nothing is kept as HopUnparsed rather than dropped,
//     because dropping it would silently shift every later hop index.
func parseTraceOutput(output string, maxHops int) *RouteResult {
	route := &RouteResult{MaxHops: maxHops, Hops: []Hop{}}
	byIndex := make(map[int]int)

	for _, line := range splitLines(output) {
		index, ok := leadingNumber(line)
		if !ok || index < 1 {
			continue
		}
		if position, exists := byIndex[index]; exists {
			parseTraceLine(&route.Hops[position], line)
			continue
		}
		route.Hops = append(route.Hops, Hop{
			Index:     index,
			TTL:       index,
			Address:   Unknown,
			Addresses: []string{},
			RttMsAll:  []float64{},
		})
		byIndex[index] = len(route.Hops) - 1
		parseTraceLine(&route.Hops[len(route.Hops)-1], line)
	}

	for i := range route.Hops {
		if route.Hops[i].State == "" {
			route.Hops[i].State = HopUnparsed
		}
		if route.Hops[i].Address == "" {
			route.Hops[i].Address = Unknown
		}
	}
	sortHops(route.Hops)
	return route
}

// parseTraceLine folds one output line into a hop.
//
// A line carries up to three probes, so every round trip on it is recorded, not
// only the first. The two programs disagree about token layout, so the reader
// handles both:
//
//   - Unix prints "0.412 ms": a number followed by its own unit token.
//   - Windows tracert prints a bare integer, or "<1 ms" when the reply came
//     back faster than the millisecond it prints.
//
// "<N" is an upper bound, not a measurement. Such a probe still counts as
// responded, because a reply genuinely arrived, but it contributes no numeric
// round trip: publishing N would overstate and publishing 0 would understate.
func parseTraceLine(hop *Hop, line string) {
	fields := strings.Fields(afterHopIndex(line))
	if len(fields) == 0 {
		hop.State = HopUnparsed
		return
	}

	var roundTrips []float64
	var pending *float64
	sawTimeout := false
	stars := 0
	bounds := 0
	hostnames := []string{}

	flush := func() {
		if pending != nil {
			roundTrips = append(roundTrips, *pending)
			pending = nil
		}
	}

	for index := 0; index < len(fields); index++ {
		field := fields[index]
		switch {
		case isAllStars(field):
			sawTimeout = true
			stars++

		case field == "ms":
			// Unit for the number just read.
			flush()

		case strings.HasPrefix(field, "<"):
			if _, err := strconv.ParseFloat(field[1:], 64); err == nil {
				bounds++
			}

		case traceAddress.MatchString(field):
			// Every responding address is kept, so a load balanced hop that
			// answers from two routers is not reported as one.
			for _, address := range traceAddress.FindAllString(field, -1) {
				hop.Addresses = appendUnique(hop.Addresses, address)
			}

		default:
			if value, err := parseTraceNumber(field); err == nil {
				if index+1 < len(fields) && fields[index+1] == "ms" {
					roundTrips = append(roundTrips, value)
					index++
					continue
				}
				// A bare number with no unit of its own is one of a run of them,
				// so the previous one is complete and this one starts a new pair.
				flush()
				held := value
				pending = &held
				continue
			}
			// A host name is only credible in one of two positions: immediately
			// before the address it belongs to, as Unix traceroute prints it
			// ("gateway (192.168.1.1)"), or as the last field of the line, as
			// Windows prints it. A word anywhere else is traceroute's own prose
			// and must never be published as a hop identity: reading
			// "Request timed out." as three host names would claim that a router
			// exists and is called "Request".
			if name := plausibleHostname(field); name != "" && credibleHostname(name, fields, index) {
				hostnames = appendUnique(hostnames, name)
			}
		}
	}
	flush()

	// Probes counts probe results actually observed on this line, so it stays
	// consistent with Responded. Counting output lines instead would report one
	// probe for a row that tracert prints three results into.
	hop.Probes += stars + bounds + len(roundTrips)

	for _, name := range hostnames {
		hop.Hostnames = appendUnique(hop.Hostnames, name)
	}
	if len(hop.Addresses) > 0 {
		hop.Address = hop.Addresses[0]
	}

	if len(roundTrips) > 0 {
		hop.State = HopResponding
		hop.Responded += len(roundTrips) + bounds
		hop.RttMsAll = append(hop.RttMsAll, roundTrips...)
		best := roundTrips[0]
		for _, value := range roundTrips {
			if value < best {
				best = value
			}
		}
		hop.RttMs = floatPtr(best)
		if bounds > 0 {
			hop.Notes = append(hop.Notes, boundNote(bounds))
		}
		return
	}

	if bounds > 0 {
		// A reply arrived but the platform published only an upper bound, so
		// the hop is known to be alive without a number to quote.
		hop.State = HopResponding
		hop.Responded += bounds
		hop.RttMsAll = nil
		hop.Notes = append(hop.Notes, boundNote(bounds))
		return
	}

	if sawTimeout {
		hop.State = HopTimeout
		return
	}
	hop.State = HopUnparsed
}

// boundNote explains an excluded upper bound.
func boundNote(count int) string {
	return "the platform reported " + strconv.Itoa(count) +
		" probe(s) only as an upper bound (\"<N ms\"); the hop is known to have responded, " +
		"but no point value is published because neither 0 nor N would be truthful"
}

// parseTraceNumber reads a numeric token, accepting a comma decimal separator.
func parseTraceNumber(field string) (float64, error) {
	return strconv.ParseFloat(strings.Replace(field, ",", ".", 1), 64)
}

// afterHopIndex removes the leading hop number and the whitespace after it.
//
// It cannot cut at the first space: both programs indent the hop number, so the
// first space is at the start of the line and cutting there would leave the hop
// number in place and make a row of stars look like an ordinary row.
func afterHopIndex(line string) string {
	trimmed := strings.TrimLeft(line, " \t")
	end := 0
	for end < len(trimmed) && trimmed[end] >= '0' && trimmed[end] <= '9' {
		end++
	}
	if end == 0 {
		return strings.TrimSpace(line)
	}
	// The prefix is only stripped when a separator actually follows the
	// number, so a line that starts with a digit but is not a hop row survives.
	if end < len(trimmed) && (trimmed[end] == ' ' || trimmed[end] == '\t') {
		return strings.TrimSpace(trimmed[end:])
	}
	return strings.TrimSpace(line)
}

// traceValue extracts the number from a field that already matched a pattern.
func traceValue(field string, pattern *regexp.Regexp) (float64, bool) {
	match := pattern.FindStringSubmatch(field)
	if match == nil {
		return 0, false
	}
	value, err := strconv.ParseFloat(strings.Replace(match[1], ",", ".", 1), 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

// hostnameCharacters are the characters a printed host name may contain.
const hostnameCharacters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-"

// tracerouteMessageWords are the words the platform traceroute uses when a hop
// does not answer, in the languages this parser has been tested against.
//
// They are rejected even though the positional rule above already excludes
// them. The positional rule depends on where a field happens to land, and these
// words are exactly what a change of Windows display language would otherwise
// turn into a hop name.
var tracerouteMessageWords = map[string]bool{
	// English.
	"request": true, "timed": true, "out": true, "timeout": true,
	"time": true, "exceeded": true, "unreachable": true,
	// Simplified Chinese, as printed by tracert on a zh-CN system.
	"请求": true, "超时": true, "不可达": true, "无响应": true,
}

// nextIsAddress reports whether the token at position is an address, which is
// the context in which a printed host name becomes credible.
func nextIsAddress(fields []string, position int) bool {
	if position < 0 || position >= len(fields) {
		return false
	}
	return traceAddress.MatchString(fields[position])
}

// credibleHostname reports whether a candidate name is believable enough to
// publish as a hop identity.
//
// Two shapes qualify and nothing else does:
//
//   - a fully qualified name, which contains a dot and therefore looks like a
//     host rather than an English word;
//   - a single label printed immediately before the address it belongs to, the
//     shape Unix traceroute uses.
//
// Requiring one of the two is what keeps traceroute's own prose out of the
// result. "Request timed out." has neither a dot nor an adjacent address, so
// none of its three words can become a hop name. The same holds for the
// localised forms such as "请求超时。", which arrive as a single token and are
// rejected by both tests.
func credibleHostname(name string, fields []string, index int) bool {
	if strings.Contains(strings.TrimSuffix(name, "."), ".") {
		return true
	}
	return nextIsAddress(fields, index+1)
}

// isNameRune reports whether r may appear in a printed host name: the ASCII set
// above, plus any non-ASCII rune, so a localised host name is not discarded
// purely for being localised.
func isNameRune(r rune) bool {
	if r > 127 {
		return true
	}
	return strings.ContainsRune(hostnameCharacters, r)
}

// plausibleHostname returns field when it could be a printed host name.
//
// A number that happens to contain a dot, such as "0.412" from a millisecond
// column, is rejected: it parses as a number, so it is a value and not a name.
// That distinction is what keeps a latency column from being published as a hop
// identity.
func plausibleHostname(field string) string {
	trimmed := strings.Trim(field, "()")
	if trimmed == "" {
		return ""
	}
	for _, r := range trimmed {
		if !isNameRune(r) {
			return ""
		}
	}
	if traceAddress.MatchString(trimmed) {
		return ""
	}
	if _, err := strconv.ParseFloat(strings.Replace(trimmed, ",", ".", 1), 64); err == nil {
		return ""
	}
	if tracerouteMessageWords[strings.ToLower(trimmed)] {
		return ""
	}
	// A name has to contain at least one letter. A purely numeric token is a
	// value that slipped past the parse above, never an identity.
	hasLetter := false
	for _, r := range trimmed {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r > 127 {
			hasLetter = true
			break
		}
	}
	if !hasLetter {
		return ""
	}
	return strings.TrimSuffix(trimmed, ".")
}

// leadingNumber reads the hop index that both programs print first.
func leadingNumber(line string) (int, bool) {
	trimmed := strings.TrimSpace(line)
	end := 0
	for end < len(trimmed) && trimmed[end] >= '0' && trimmed[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	value, err := strconv.Atoi(trimmed[:end])
	if err != nil {
		return 0, false
	}
	return value, true
}

// isAllStars reports whether a field is only timeout markers.
func isAllStars(field string) bool {
	trimmed := strings.TrimSpace(field)
	if trimmed == "" {
		return false
	}
	for _, r := range trimmed {
		if r != '*' {
			return false
		}
	}
	return true
}

// sortHops orders hops by index with an insertion sort. A trace produces at
// most a few dozen hops, so this avoids importing sort for one call site.
func sortHops(hops []Hop) {
	for i := 1; i < len(hops); i++ {
		current := hops[i]
		j := i - 1
		for j >= 0 && hops[j].Index > current.Index {
			hops[j+1] = hops[j]
			j--
		}
		hops[j+1] = current
	}
}
