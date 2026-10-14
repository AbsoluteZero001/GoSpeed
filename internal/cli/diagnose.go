package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/netdiag"
)

// runDiagnose implements "gospeed diagnose".
//
// The command is a presentation layer only: it parses flags, calls
// internal/netdiag and renders the result. No diagnostic logic lives here, the
// same rule the test command follows.
func (a *App) runDiagnose(ctx context.Context, args []string) int {
	set := a.newFlagSet("gospeed diagnose")
	var (
		target     = set.String("target", "", "host name or IP address to diagnose (required)")
		port       = set.Int("port", netdiag.DefaultPort, "TCP port used by the TCP connect measurement")
		samples    = set.Int("samples", netdiag.DefaultSamples, "probes issued per latency measurement")
		timeout    = set.Duration("timeout", netdiag.DefaultTimeout, "per probe timeout")
		maxHops    = set.Int("max-hops", netdiag.DefaultMaxHops, "traceroute TTL ceiling")
		icmp       = set.Bool("icmp", true, "measure ICMP echo round trip")
		tcp        = set.Bool("tcp", true, "measure TCP connect round trip")
		httpProbe  = set.Bool("http", false, "measure HTTP round trip (off by default; needs an HTTP endpoint)")
		httpURL    = set.String("http-url", "", "URL for the HTTP measurement, defaults to http://<target>/")
		traceroute = set.Bool("traceroute", true, "run an IPv4 traceroute")
		enrich     = set.Bool("asn", false, "look up ASN ownership for public addresses (queries a third party)")
		asJSON     = set.Bool("json", false, "print the structured result as JSON")
	)
	if err := set.Parse(args); err != nil {
		return 2
	}
	if *target == "" && set.NArg() > 0 {
		*target = set.Arg(0)
	}
	if *target == "" {
		return a.fail("--target is required, for example: gospeed diagnose --target example.com")
	}
	if *samples <= 0 {
		return a.fail("--samples must be positive, got %d", *samples)
	}
	if *timeout <= 0 {
		return a.fail("--timeout must be positive, got %s", *timeout)
	}

	options := netdiag.Options{
		Target:     *target,
		Port:       *port,
		Samples:    *samples,
		Timeout:    *timeout,
		MaxHops:    *maxHops,
		ICMP:       *icmp,
		TCP:        *tcp,
		HTTP:       *httpProbe,
		HTTPURL:    *httpURL,
		Traceroute: *traceroute,
		Enrich:     *enrich,
	}
	if *enrich {
		options.ASN = netdiag.NewRIPEstatEnricher()
	}

	diagnosis, err := netdiag.Diagnose(ctx, options)
	if err != nil {
		if ctx.Err() != nil {
			return a.fail("diagnose cancelled: %v", err)
		}
		return a.fail("diagnose: %v", err)
	}

	if *asJSON {
		return a.encodeJSON(diagnosis)
	}
	a.printDiagnosis(diagnosis)
	if len(diagnosis.Errors) > 0 {
		return 1
	}
	return 0
}

// printDiagnosis renders the human readable summary.
//
// The order mirrors how a reader reasons about a path: first where the packet
// starts, then where it ends up, then how long each hop took.
func (a *App) printDiagnosis(d *netdiag.Diagnosis) {
	fmt.Fprintf(a.Out, "GoSpeed diagnose - schema %s\n", d.SchemaVersion)
	fmt.Fprintf(a.Out, "生成时间: %s\n\n", d.GeneratedAt.Format(time.RFC3339))

	fmt.Fprintln(a.Out, "== 目标 ==")
	t := d.Target
	fmt.Fprintf(a.Out, "  输入        : %s\n", t.Input)
	fmt.Fprintf(a.Out, "  解析        : %s\n", resolutionText(t))
	if len(t.Addresses) > 0 {
		fmt.Fprintf(a.Out, "  地址        : %s\n", strings.Join(t.Addresses, ", "))
	}
	fmt.Fprintf(a.Out, "  TCP 端口    : %d\n", t.Port)
	if t.Detail != "" {
		fmt.Fprintf(a.Out, "  说明        : %s\n", t.Detail)
	}

	fmt.Fprintln(a.Out, "\n== 本机网络 ==")
	n := d.Network
	fmt.Fprintf(a.Out, "  默认接口    : %s\n", n.DefaultInterface)
	fmt.Fprintf(a.Out, "  默认网关    : %s\n", n.DefaultGateway)
	if len(n.DNSServers) > 0 {
		fmt.Fprintf(a.Out, "  DNS 服务器  : %s\n", strings.Join(n.DNSServers, ", "))
	} else {
		fmt.Fprintln(a.Out, "  DNS 服务器  : unknown")
	}
	if r := n.RouteToTarget; r != nil {
		fmt.Fprintf(a.Out, "  到目标的路由: 源地址 %s，接口 %s\n", r.LocalAddress, r.Interface)
		fmt.Fprintf(a.Out, "                （%s；这是操作系统选择的路由，不是猜测）\n", r.Method)
	} else {
		fmt.Fprintln(a.Out, "  到目标的路由: unknown")
	}
	fmt.Fprintf(a.Out, "  目标范围    : %s\n", n.Scope)
	fmt.Fprintf(a.Out, "  本地地址范围: %s\n", n.LocalScope)
	if len(n.Interfaces) > 0 {
		fmt.Fprintf(a.Out, "  接口数量    : %d\n", len(n.Interfaces))
	}
	for _, note := range n.Notes {
		fmt.Fprintf(a.Out, "  注          : %s\n", note)
	}

	fmt.Fprintln(a.Out, "\n== 延迟 ==")
	fmt.Fprintln(a.Out, "  三种口径分别测量，互不换算，也不能互相替代。")
	printLatency(a.Out, "ICMP echo", d.Latency.ICMP)
	printLatency(a.Out, "TCP connect", d.Latency.TCP)
	printLatency(a.Out, "HTTP RTT   ", d.Latency.HTTP)

	if r := d.Route; r != nil {
		fmt.Fprintln(a.Out, "\n== 路由（正向探测，非往返路径）==")
		fmt.Fprintf(a.Out, "  方法        : %s\n", r.Method)
		fmt.Fprintf(a.Out, "  TTL 上限    : %d\n", r.MaxHops)
		if r.Detail != "" {
			fmt.Fprintf(a.Out, "  说明        : %s\n", r.Detail)
		}
		if len(r.Hops) > 0 {
			fmt.Fprintln(a.Out, "  hop  跳数  地址                  RTT(ms)  状态")
			for _, hop := range r.Hops {
				fmt.Fprintf(a.Out, "  %3d  %4d  %-21s %8s  %s\n",
					hop.Index, hop.TTL, hop.Address, rttText(hop.RttMs), hop.State)
			}
		}
		fmt.Fprintf(a.Out, "  到达目标    : %s\n", boolText(r.TargetReached))
		for _, note := range r.Notes {
			fmt.Fprintf(a.Out, "  注          : %s\n", note)
		}
	}

	if len(d.Enrichment) > 0 {
		fmt.Fprintln(a.Out, "\n== ASN 归属（第三方数据，不代表转发路径）==")
		for _, info := range d.Enrichment {
			fmt.Fprintf(a.Out, "  %-16s AS%-10d %s\n", info.Address, info.ASN, info.Holder)
			if info.Prefix != "" {
				fmt.Fprintf(a.Out, "  %-16s 前缀 %s（来源 %s）\n", "", info.Prefix, info.Source)
			}
			if info.Detail != "" {
				fmt.Fprintf(a.Out, "  %-16s 说明 %s\n", "", info.Detail)
			}
		}
	}

	if len(d.Warnings) > 0 {
		fmt.Fprintln(a.Out, "\n== 警告 ==")
		for _, warning := range d.Warnings {
			fmt.Fprintf(a.Out, "  - %s\n", warning)
		}
	}
	if len(d.Errors) > 0 {
		fmt.Fprintln(a.Out, "\n== 错误（未完成的测量）==")
		for _, message := range d.Errors {
			fmt.Fprintf(a.Out, "  - %s\n", message)
		}
	}
}

// printLatency renders one measurement method.
func printLatency(w interface{ Write([]byte) (int, error) }, label string, m *netdiag.LatencyMeasurement) {
	if m == nil {
		fmt.Fprintf(w, "  %s %s\n", label, netdiag.NotApplicable)
		return
	}
	if m.Outcome != netdiag.LatencyMeasured {
		detail := m.Detail
		if detail == "" {
			detail = string(m.Outcome)
		}
		fmt.Fprintf(w, "  %s %s (%s)\n", label, netdiag.NotApplicable, detail)
		return
	}
	fmt.Fprintf(w, "  %s min %s / 平均 %s / max %s / jitter %s\n",
		label,
		msText(m.MinMs), msText(m.AverageMs), msText(m.MaxMs), msText(m.JitterMs))
	fmt.Fprintf(w, "  %s 成功 %d，超时 %d，失败 %d，共 %d 次\n",
		strings.Repeat(" ", len(label)), m.Successful, m.TimedOut, m.Failed, m.Attempts)
	for _, note := range m.Notes {
		fmt.Fprintf(w, "  %s 注 %s\n", strings.Repeat(" ", len(label)), note)
	}
}

// msText renders a millisecond value or the not applicable marker. A measured
// zero prints as a number, which is the point: it was measured.
func msText(v *float64) string {
	if v == nil {
		return netdiag.NotApplicable
	}
	return fmt.Sprintf("%.2f ms", *v)
}

// rttText renders a hop round trip.
func rttText(v *float64) string {
	if v == nil {
		return netdiag.NotApplicable
	}
	return fmt.Sprintf("%.2f", *v)
}

// boolText renders a boolean plainly.
func boolText(v bool) string {
	if v {
		return "是"
	}
	return "否"
}

// resolutionText states the resolver outcome without implying success.
func resolutionText(t *netdiag.TargetInfo) string {
	if t.Resolved {
		return "成功"
	}
	return "失败"
}
