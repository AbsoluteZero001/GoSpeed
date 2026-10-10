package provider

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// decode marshals v and returns both the raw JSON text and a generic map so
// tests can assert exact key names, null rendering and value formatting.
func decode(t *testing.T, v any) (string, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return string(raw), generic
}

func mustFloat(v float64) *float64 { return &v }

// TestFinalResultJSONRoundTrip pins the full shape of a completed result:
// key names, nested structures and a lossless decode back into the model.
func TestFinalResultJSONRoundTrip(t *testing.T) {
	result := FinalResult{
		ProviderID: IDGoSpeedHTTP,
		ServerID:   "lab-node-1",
		Status:     StatusCompleted,
		Download: &Rate{
			Mbps:             mustFloat(83.41625),
			ElapsedMs:        10391,
			TransferredBytes: 108005302,
			Window:           "shared_window_first_response_byte_to_last_body_byte",
			StopReason:       "duration_elapsed",
		},
		Upload: &Rate{
			Mbps:             mustFloat(41.5),
			ElapsedMs:        10220,
			TransferredBytes: 53000000,
			Window:           "shared_window_first_body_write_to_last_server_confirmation",
			StopReason:       "duration_elapsed",
		},
		Latency: &LatencyMetric{
			ValueMs: mustFloat(12.75),
			Type:    MetricHTTPRTT,
			Source:  "http request to first response byte",
		},
		Jitter: &JitterMetric{
			ValueMs: mustFloat(0.8125),
			Type:    JitterConsecutiveDiff,
			Source:  "mean absolute difference of consecutive http_rtt samples",
		},
		ElapsedMs:        22900,
		TransferredBytes: 161005302,
		Quality: Quality{
			Completeness: Complete,
			Stability:    "normal",
			Trust:        "verified",
			Notes:        []string{"loopback fixture"},
		},
		Method: MethodRecord{
			Protocol: "gospeed-http",
			NegotiatedCaps: CapabilitySet{
				Supported: map[Capability]bool{
					CapDownload: true,
					CapUpload:   true,
					CapLatency:  true,
					CapJitter:   true,
				},
				Notes: map[Capability]string{
					CapLatency: "http_rtt",
					CapJitter:  "consecutive_diff",
				},
			},
			Budget:   BudgetLimits{DownloadBytes: 25165824, UploadBytes: 16777216, TotalBytes: 41943040},
			Warnings: []string{"loopback measurement, not internet bandwidth"},
		},
	}

	raw, generic := decode(t, result)

	for _, key := range []string{
		"providerId", "serverId", "status", "download", "upload", "latency",
		"jitter", "elapsedMs", "transferredBytes", "quality", "method",
	} {
		if _, ok := generic[key]; !ok {
			t.Fatalf("final result JSON is missing key %q: %s", key, raw)
		}
	}
	download, ok := generic["download"].(map[string]any)
	if !ok {
		t.Fatalf("download is not an object: %s", raw)
	}
	for _, key := range []string{"mbps", "elapsedMs", "transferredBytes", "window", "stopReason"} {
		if _, ok := download[key]; !ok {
			t.Fatalf("rate JSON is missing key %q: %s", key, raw)
		}
	}
	latency, ok := generic["latency"].(map[string]any)
	if !ok {
		t.Fatalf("latency is not an object: %s", raw)
	}
	for _, key := range []string{"valueMs", "type", "source"} {
		if _, ok := latency[key]; !ok {
			t.Fatalf("latency metric JSON is missing key %q: %s", key, raw)
		}
	}

	var back FinalResult
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatalf("unmarshal into FinalResult: %v", err)
	}
	if !reflect.DeepEqual(result, back) {
		t.Fatalf("round trip changed the result:\n got %+v\nwant %+v", back, result)
	}
}

// TestNullMetricsStayNull pins that missing metrics are JSON null, both on
// the value inside a metric struct and on the whole metric, and that a null
// survives a decode as nil instead of becoming a zero value.
func TestNullMetricsStayNull(t *testing.T) {
	result := FinalResult{
		ProviderID: IDMLabNDT7,
		ServerID:   "ndt-mlab2-hkg03",
		Status:     StatusCompleted,
		// Download measured; upload, latency and jitter missing.
		Download: &Rate{
			Mbps:             mustFloat(1.51955),
			ElapsedMs:        10131,
			TransferredBytes: 1922048,
			Window:           "first_response_byte_to_last_body_byte",
			StopReason:       "duration_elapsed",
		},
		Quality: Quality{Completeness: Partial, Stability: "unknown", Trust: "normal"},
		Method:  MethodRecord{Protocol: "ndt7"},
	}

	raw, generic := decode(t, result)

	if got, ok := generic["upload"]; ok && got != nil {
		t.Fatalf("upload = %v, want JSON null", got)
	}
	if !strings.Contains(raw, `"upload":null`) {
		t.Fatalf("upload must serialize as null: %s", raw)
	}
	if !strings.Contains(raw, `"latency":null`) || !strings.Contains(raw, `"jitter":null`) {
		t.Fatalf("latency and jitter must serialize as null: %s", raw)
	}
	rate := generic["download"].(map[string]any)
	if _, exists := rate["stopReason"]; !exists {
		t.Fatalf("empty stopReason must stay an explicit key with omitempty absent value, got none: %s", raw)
	}

	var back FinalResult
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Upload != nil || back.Latency != nil || back.Jitter != nil {
		t.Fatalf("null metrics decoded as values: %+v", back)
	}
	if back.Download == nil || back.Download.Mbps == nil || *back.Download.Mbps != 1.51955 {
		t.Fatalf("download rate lost in decode: %+v", back.Download)
	}
}

// TestMbpsPrecisionKeepsSixDecimals pins that rates survive JSON as real
// Mbit/s with their precision intact (the P0-L regression value: 2 MiB over
// 10391 ms is 1.614591 Mbit/s).
func TestMbpsPrecisionKeepsSixDecimals(t *testing.T) {
	rate := Rate{Mbps: mustFloat(1.614591), ElapsedMs: 10391, TransferredBytes: 2097152, Window: "first_response_byte_to_last_body_byte"}
	raw, generic := decode(t, rate)
	if !strings.Contains(raw, "1.614591") {
		t.Fatalf("mbps lost precision in JSON: %s", raw)
	}
	if got := generic["mbps"].(float64); got != 1.614591 {
		t.Fatalf("mbps = %v, want 1.614591", got)
	}
}

// TestMetricTypesSeparateQuantityFromProtocol pins the corrected metric
// vocabulary: types name quantities, sources name protocols and derivation.
func TestMetricTypesSeparateQuantityFromProtocol(t *testing.T) {
	cases := []struct {
		value MetricType
		want  string
	}{
		{MetricHTTPRTT, "http_rtt"},
		{MetricTCPMinRTT, "tcp_min_rtt"},
		{MetricSummaryRTT, "summary_rtt"},
		{MetricICMPPing, "icmp_ping"},
	}
	for _, c := range cases {
		if string(c.value) != c.want {
			t.Fatalf("metric type = %q, want %q", c.value, c.want)
		}
	}
	for _, forbidden := range []string{"ndt7", "cloudflare"} {
		for _, c := range cases {
			if strings.Contains(string(c.value), forbidden) {
				t.Fatalf("metric type %q mixes a protocol name into a quantity", c.value)
			}
		}
	}

	latency := LatencyMetric{ValueMs: mustFloat(360.83), Type: MetricTCPMinRTT, Source: "ndt7 tcp_info.MinRTT"}
	raw, _ := decode(t, latency)
	var back LatencyMetric
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(latency, back) {
		t.Fatalf("latency round trip changed the metric: %+v vs %+v", back, latency)
	}

	jitter := JitterMetric{ValueMs: mustFloat(2.25), Type: JitterRTP, Source: "rfc3550 over icmp echo series"}
	raw, _ = decode(t, jitter)
	var backJitter JitterMetric
	if err := json.Unmarshal([]byte(raw), &backJitter); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(jitter, backJitter) {
		t.Fatalf("jitter round trip changed the metric: %+v vs %+v", backJitter, jitter)
	}
	if JitterConsecutiveDiff != "consecutive_diff" || JitterRTP != "rtp_jitter" {
		t.Fatal("jitter type constants drifted")
	}
}

// TestStatusAndCompletenessRoundTrip covers every terminal status (including
// the ones this round only models: timeout, budget_exceeded,
// consent_required) and the completeness values, with the documented
// convention that a budget exceeded result carries incomplete evidence.
func TestStatusAndCompletenessRoundTrip(t *testing.T) {
	statuses := []MeasurementStatus{
		StatusCompleted,
		StatusFailed,
		StatusCancelled,
		StatusTimeout,
		StatusBudgetExceeded,
		StatusConsentNeeded,
	}
	for _, status := range statuses {
		raw, _ := decode(t, FinalResult{Status: status})
		var back FinalResult
		if err := json.Unmarshal([]byte(raw), &back); err != nil {
			t.Fatalf("unmarshal %s: %v", status, err)
		}
		if back.Status != status {
			t.Fatalf("status = %q, want %q", back.Status, status)
		}
	}
	want := map[MeasurementStatus]string{
		StatusCompleted:      "completed",
		StatusFailed:         "failed",
		StatusCancelled:      "cancelled",
		StatusTimeout:        "timeout",
		StatusBudgetExceeded: "budget_exceeded",
		StatusConsentNeeded:  "consent_required",
	}
	for status, literal := range want {
		raw, _ := decode(t, FinalResult{Status: status})
		if !strings.Contains(raw, `"status":"`+literal+`"`) {
			t.Fatalf("status %q did not serialize as %q: %s", status, literal, raw)
		}
	}

	completeness := map[Completeness]string{
		Complete:   "complete",
		Partial:    "partial",
		Incomplete: "incomplete",
	}
	for value, literal := range completeness {
		quality := Quality{Completeness: value, Stability: "normal", Trust: "normal"}
		raw, _ := decode(t, quality)
		if !strings.Contains(raw, `"completeness":"`+literal+`"`) {
			t.Fatalf("completeness %q did not serialize as %q: %s", value, literal, raw)
		}
		var back Quality
		if err := json.Unmarshal([]byte(raw), &back); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if back.Completeness != value {
			t.Fatalf("completeness = %q, want %q", back.Completeness, value)
		}
	}

	// Documented convention: a budget stop is a partial evidence outcome.
	budget := FinalResult{
		Status:    StatusBudgetExceeded,
		Quality:   Quality{Completeness: Incomplete, Stability: "unknown", Trust: "normal"},
		ErrorCode: "budget_exceeded",
	}
	raw, _ := decode(t, budget)
	var back FinalResult
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Status != StatusBudgetExceeded || back.Quality.Completeness != Incomplete {
		t.Fatalf("budget exceeded result = %+v, want budget_exceeded + incomplete", back)
	}
}

// TestErrorFieldsRoundTrip pins that the error code stays machine matchable
// and the message stays human readable, and that both vanish when empty.
func TestErrorFieldsRoundTrip(t *testing.T) {
	withError := FinalResult{
		Status:       StatusFailed,
		ErrorCode:    "remote_gate_closed",
		ErrorMessage: "the remote gate rejected this run",
	}
	raw, generic := decode(t, withError)
	if generic["errorCode"] != "remote_gate_closed" || generic["errorMessage"] != "the remote gate rejected this run" {
		t.Fatalf("error fields lost: %s", raw)
	}
	var back FinalResult
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.ErrorCode != "remote_gate_closed" || back.ErrorMessage != "the remote gate rejected this run" {
		t.Fatalf("error fields changed in decode: %+v", back)
	}

	withoutError, generic := decode(t, FinalResult{Status: StatusCompleted})
	if _, exists := generic["errorCode"]; exists {
		t.Fatalf("empty errorCode must be omitted: %s", withoutError)
	}
	if _, exists := generic["errorMessage"]; exists {
		t.Fatalf("empty errorMessage must be omitted: %s", withoutError)
	}
}

// TestMethodRecordSerializesNegotiatedCaps pins the corrected MethodRecord:
// the negotiated capability set is exported and travels with the result.
func TestMethodRecordSerializesNegotiatedCaps(t *testing.T) {
	record := MethodRecord{
		Protocol: "ndt7",
		NegotiatedCaps: CapabilitySet{
			Supported: map[Capability]bool{CapDownload: true, CapLatency: true},
			Notes:     map[Capability]string{CapLatency: "tcp_min_rtt"},
		},
		Budget:         BudgetLimits{DownloadBytes: 25165824, UploadBytes: 16777216, TotalBytes: 41943040},
		ConsentVersion: "2026-10-10",
		Warnings:       []string{"tcp rttvar observed but not reported as jitter"},
	}
	raw, generic := decode(t, record)

	caps, ok := generic["negotiatedCaps"].(map[string]any)
	if !ok {
		t.Fatalf("negotiatedCaps missing or not an object: %s", raw)
	}
	supported, ok := caps["supported"].(map[string]any)
	if !ok || supported["download"] != true || supported["latency"] != true {
		t.Fatalf("supported capabilities lost: %s", raw)
	}
	if _, exists := supported["jitter"]; exists {
		t.Fatalf("unsupported capability must not appear as true: %s", raw)
	}
	if generic["consentVersion"] != "2026-10-10" {
		t.Fatalf("consentVersion lost: %s", raw)
	}

	var back MethodRecord
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !back.NegotiatedCaps.Supports(CapDownload) || !back.NegotiatedCaps.Supports(CapLatency) {
		t.Fatalf("negotiated caps lost in decode: %+v", back.NegotiatedCaps)
	}
	if back.NegotiatedCaps.Supports(CapJitter) {
		t.Fatal("jitter must not be negotiated when the provider cannot measure it")
	}
	if back.NegotiatedCaps.Note(CapLatency) != "tcp_min_rtt" {
		t.Fatalf("latency note = %q, want tcp_min_rtt", back.NegotiatedCaps.Note(CapLatency))
	}
	if back.Budget.TotalBytes != 41943040 {
		t.Fatalf("budget lost: %+v", back.Budget)
	}
	if !reflect.DeepEqual(record, back) {
		t.Fatalf("round trip changed the record:\n got %+v\nwant %+v", back, record)
	}
}

// TestCapabilitySetJSONRoundTrip covers the set on its own, including the
// opt-in semantics of Supports.
func TestCapabilitySetJSONRoundTrip(t *testing.T) {
	set := CapabilitySet{
		Supported: map[Capability]bool{
			CapDownload: true,
			CapUpload:   true,
			CapLatency:  true,
			CapJitter:   false, // explicitly negotiated as not available
		},
		Notes: map[Capability]string{CapLatency: "http_rtt"},
	}
	raw, _ := decode(t, set)

	var back CapabilitySet
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(set, back) {
		t.Fatalf("round trip changed the set:\n got %+v\nwant %+v", back, set)
	}
	if !back.Supports(CapDownload) || back.Supports(CapJitter) || back.Supports(CapPacketLoss) {
		t.Fatalf("supports semantics broken: %+v", back.Supported)
	}

	empty, generic := decode(t, CapabilitySet{})
	if _, exists := generic["supported"]; !exists {
		t.Fatalf("empty supported map must stay an explicit null field: %s", empty)
	}
	if _, exists := generic["notes"]; exists {
		t.Fatalf("empty notes must be omitted: %s", empty)
	}
}

// TestNodeSplitsOwnershipFromDiscovery pins that a node states who operates
// it and how it was found as two independent fields, with no legacy "kind".
func TestNodeSplitsOwnershipFromDiscovery(t *testing.T) {
	node := Node{
		ID:        "ndt-mlab2-hkg03",
		Name:      "M-Lab Hong Kong 03",
		Endpoint:  "wss://ndt-mlab2-hkg03.mlab-oti.measurement-lab.org:443",
		Scope:     ScopeRemote,
		Provider:  IDMLabNDT7,
		Ownership: OwnershipThirdParty,
		Discovery: DiscoveryService,
	}
	raw, generic := decode(t, node)

	if _, exists := generic["kind"]; exists {
		t.Fatalf("legacy kind key must not exist: %s", raw)
	}
	if generic["ownership"] != "third_party" || generic["discovery"] != "service" {
		t.Fatalf("ownership/discovery lost: %s", raw)
	}

	var back Node
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(node, back) {
		t.Fatalf("round trip changed the node:\n got %+v\nwant %+v", back, node)
	}
}
