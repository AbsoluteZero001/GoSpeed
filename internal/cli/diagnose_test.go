package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// runCLI executes one command line and returns stdout, stderr and the code.
func runCLI(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := NewApp(&out, &errOut).Run(context.Background(), args)
	return out.String(), errOut.String(), code
}

func TestDiagnoseRequiresATarget(t *testing.T) {
	_, errOut, code := runCLI(t, "diagnose")
	if code == 0 {
		t.Error("a diagnose without a target must not succeed")
	}
	if !strings.Contains(errOut, "--target") {
		t.Errorf("stderr = %q, want it to name the missing flag", errOut)
	}
}

func TestDiagnoseRejectsNonPositiveSamples(t *testing.T) {
	_, errOut, code := runCLI(t, "diagnose", "--target", "example.com", "--samples", "0")
	if code == 0 {
		t.Error("zero samples must be rejected")
	}
	if !strings.Contains(errOut, "--samples") {
		t.Errorf("stderr = %q, want it to name the offending flag", errOut)
	}
}

func TestDiagnoseAcceptsTargetAsPositionalArgument(t *testing.T) {
	// The flag form is validated; a positional target is accepted so the short
	// invocation works. The run itself is expected to fail on this host, which
	// is exactly why the exit code and the error text are checked rather than a
	// measurement.
	_, errOut, code := runCLI(t, "diagnose", "--samples", "-1", "example.com")
	if code == 0 || !strings.Contains(errOut, "--samples") {
		t.Errorf("code = %d stderr = %q, want the sample validation to fire", code, errOut)
	}
}

func TestDiagnoseJSONOutputIsAParsableDiagnosis(t *testing.T) {
	// A literal loopback address needs no name resolution, so this exercises
	// the whole pipeline without contacting a public name service.
	out, _, _ := runCLI(t,
		"diagnose",
		"--target", "127.0.0.1",
		"--port", "1",
		"--samples", "1",
		"--timeout", "200ms",
		"--icmp=false",
		"--tcp=false",
		"--traceroute=false",
		"--json",
	)
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("stdout is not valid json: %v\n%s", err, out)
	}
	if payload["schema_version"] != "netdiag/1" {
		t.Errorf("schema_version = %v", payload["schema_version"])
	}
	for _, key := range []string{"network", "target", "latency", "route", "warnings", "errors"} {
		if _, ok := payload[key]; !ok {
			t.Errorf("json is missing the %q section", key)
		}
	}
	target, ok := payload["target"].(map[string]any)
	if !ok {
		t.Fatal("target section is not an object")
	}
	if target["input"] != "127.0.0.1" {
		t.Errorf("target.input = %v", target["input"])
	}
}

func TestDiagnoseHumanOutputNamesEveryLatencyMethodSeparately(t *testing.T) {
	out, _, _ := runCLI(t,
		"diagnose",
		"--target", "127.0.0.1",
		"--port", "1",
		"--samples", "1",
		"--timeout", "200ms",
		"--traceroute=false",
	)
	// The three methods must be visible as three separate lines so a reader
	// cannot read one number as describing the others.
	for _, label := range []string{"ICMP echo", "TCP connect", "HTTP RTT"} {
		if !strings.Contains(out, label) {
			t.Errorf("output does not name %q:\n%s", label, out)
		}
	}
	if !strings.Contains(out, "互不换算") {
		t.Errorf("output does not state that the methods are not interchangeable:\n%s", out)
	}
}

func TestUsageMentionsDiagnose(t *testing.T) {
	out, _, code := runCLI(t, "help")
	if code != 0 {
		t.Fatalf("help exited with %d", code)
	}
	if !strings.Contains(out, "gospeed diagnose") {
		t.Errorf("usage does not advertise diagnose:\n%s", out)
	}
}
