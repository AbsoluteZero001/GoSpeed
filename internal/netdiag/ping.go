package netdiag

import (
	"context"
	"regexp"
	"strconv"
	"time"
)

// pingRunner executes the platform ping program and returns its raw output.
//
// It is a function rather than an interface because every supported platform
// needs the same thing from a different binary; the differences are argument
// spellings, not behaviour.
type pingRunner func(ctx context.Context, host string, count int, perProbe, total time.Duration) (string, error)

// pingRTT matches the round trip the platform programs print.
//
// Only the "time=" form is a point value. The "time<N ms" form that Windows
// prints for a very fast reply is an upper bound, and the character class here
// deliberately excludes "<" so that form can never be read as a measurement.
var pingRTT = regexp.MustCompile(`time=\s*([0-9]+(?:[.,][0-9]+)?)\s*ms`)

// pingBound matches the "less than" form. A platform that prints it only
// published an upper bound, which is not a measurement and must not be turned
// into one.
var pingBound = regexp.MustCompile(`time<\s*([0-9]+(?:[.,][0-9]+)?)\s*ms`)

// parsePingOutput turns platform ping output into a SampleSet.
//
// Every platform prints a summary line such as "3 packets transmitted, 3
// received, 0% packet loss". Those percentages are deliberately not copied
// into the result: they describe ICMP echo delivery to one host and are not a
// statement about TCP or about the user's link, so publishing them here would
// invite exactly the misreading this package exists to prevent.
func parsePingOutput(output string, requested int) SampleSet {
	set := SampleSet{}
	for _, match := range pingRTT.FindAllStringSubmatch(output, -1) {
		value, err := strconv.ParseFloat(normalizeDecimal(match[1]), 64)
		if err != nil {
			continue
		}
		set.Samples = append(set.Samples, time.Duration(value*float64(time.Millisecond)))
	}
	if bounds := len(pingBound.FindAllStringSubmatch(output, -1)); bounds > 0 {
		set.Failed += bounds
		set.Detail = "the platform reported " + strconv.Itoa(bounds) +
			" probe(s) only as an upper bound (\"time<Nms\") and gave no point value; " +
			"they are excluded from the statistics rather than assumed to be 0"
	}
	answered := len(set.Samples)
	if unanswered := requested - answered - set.Failed; unanswered > 0 {
		set.TimedOut = unanswered
		if set.Detail == "" {
			set.Detail = strconv.Itoa(unanswered) + " of " + strconv.Itoa(requested) +
				" probes were not answered; an unanswered ICMP echo is not packet loss, " +
				"it only means this host did not reply to this probe"
		}
	}
	if answered == 0 && set.Detail == "" {
		set.Detail = "no ICMP round trip was reported"
	}
	return set
}

// normalizeDecimal turns a comma decimal separator into a dot so one regex
// serves every locale.
func normalizeDecimal(value string) string {
	for i := 0; i < len(value); i++ {
		if value[i] == ',' {
			return value[:i] + "." + value[i+1:]
		}
	}
	return value
}
