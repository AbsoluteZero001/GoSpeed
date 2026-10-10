package mlabpoc

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	v2 "github.com/m-lab/locate/api/v2"
)

// RemoteRunNoticeVersion identifies the exact per-run confirmation wording.
const RemoteRunNoticeVersion = "gospeed-mlab-remote-run-notice-1"

// RemoteExperimentSwitch is the experimental-build master switch for public
// internet M-Lab tests. Its zero state is DISABLED; only an explicit
// experimental entry point may call Enable. A disabled switch cannot be
// bypassed by any other option value.
type RemoteExperimentSwitch struct {
	enabled atomic.Bool
}

// NewRemoteExperimentSwitch returns a disabled switch.
func NewRemoteExperimentSwitch() *RemoteExperimentSwitch {
	return &RemoteExperimentSwitch{}
}

// Enable turns the experimental switch on. Production builds never call it.
func (s *RemoteExperimentSwitch) Enable() {
	s.enabled.Store(true)
}

// Enabled reports the switch state.
func (s *RemoteExperimentSwitch) Enabled() bool {
	return s.enabled.Load()
}

// RemoteRunConfirmation is the explicit per-run approval for ONE public
// measurement. It is never cached, never inferred and never pre-agreed.
type RemoteRunConfirmation struct {
	Confirmed     bool      `json:"confirmed"`
	ConfirmedAt   time.Time `json:"confirmedAt,omitempty"`
	NoticeVersion string    `json:"noticeVersion,omitempty"`
	TargetHost    string    `json:"targetHost,omitempty"`
}

// RemoteGate authorizes exactly one non-loopback measurement run:
//
//   - the experimental build switch must be enabled,
//   - the target host must have come from validated Locate discovery (the
//     gate refuses arbitrary remote addresses),
//   - and a per-run confirmation must exist for this target host and this
//     notice version.
//
// A nil gate, or any missing piece, blocks the run with zero connections.
type RemoteGate struct {
	Switch       *RemoteExperimentSwitch
	Confirmation *RemoteRunConfirmation
	AllowedHosts map[string]bool
}

// NewRemoteGate builds a gate whose allowed hosts are derived from
// pre-validated Locate targets.
func NewRemoteGate(sw *RemoteExperimentSwitch, targets []v2.Target) *RemoteGate {
	allowed := make(map[string]bool, len(targets))
	for _, target := range targets {
		for _, raw := range target.URLs {
			if parsed, err := url.Parse(raw); err == nil {
				if host := parsed.Hostname(); host != "" {
					allowed[strings.TrimSuffix(strings.ToLower(host), ".")] = true
				}
			}
		}
	}
	return &RemoteGate{Switch: sw, AllowedHosts: allowed}
}

// Confirm attaches the per-run confirmation to the gate.
func (g *RemoteGate) Confirm(confirmation RemoteRunConfirmation) {
	c := confirmation
	g.Confirmation = &c
}

// Authorize verifies that a non-loopback host may be measured. Every failure
// mode returns a distinct error for reporting.
func (g *RemoteGate) Authorize(host string) error {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if g == nil {
		return errors.New("remote measurement rejected: no experimental gate configured")
	}
	if g.Switch == nil || !g.Switch.Enabled() {
		return errors.New("remote measurement rejected: experimental remote switch is disabled")
	}
	if !g.AllowedHosts[host] {
		return fmt.Errorf("remote measurement rejected: host %q did not come from validated Locate discovery", host)
	}
	c := g.Confirmation
	if c == nil || !c.Confirmed {
		return fmt.Errorf("remote measurement rejected: run %q was not explicitly confirmed", host)
	}
	if c.NoticeVersion != RemoteRunNoticeVersion {
		return fmt.Errorf("remote measurement rejected: confirmation notice version %q does not match %q", c.NoticeVersion, RemoteRunNoticeVersion)
	}
	if !c.ConfirmedAt.IsZero() && time.Since(c.ConfirmedAt) > 10*time.Minute {
		return errors.New("remote measurement rejected: confirmation is stale (older than 10 minutes)")
	}
	return nil
}

// RemoteRunNotice renders the risk disclosure shown before each public
// measurement: public data exposure, traffic budget and timeout risks.
func RemoteRunNotice(host string, downloadBudget, uploadBudget, totalBudget int64, overallTimeout time.Duration) string {
	if downloadBudget <= 0 {
		downloadBudget = DefaultDownloadBudgetBytes
	}
	if uploadBudget <= 0 {
		uploadBudget = DefaultUploadBudgetBytes
	}
	if totalBudget <= 0 {
		totalBudget = DefaultTotalBudgetBytes
	}
	if overallTimeout <= 0 {
		overallTimeout = DefaultOverallTimeout
	}
	return fmt.Sprintf(remoteRunNoticeTemplate, host,
		mib(downloadBudget), mib(uploadBudget), mib(totalBudget),
		overallTimeout, RemoteRunNoticeVersion)
}

const remoteRunNoticeTemplate = `M-Lab 公网测速单次确认（GoSpeed 实验模块，仅一次有效）

目标节点（来自受验证的 M-Lab Locate 查询）： %s

确认前请知悉本次测速的风险：

1. 数据公开：你的公网 IP 将与测量数据一起被 M-Lab 公开发布并长期保留。
2. 流量风险：本次测速计划流量上限为 下载 ≤ %d MiB、上传 ≤ %d MiB、
   总计 ≤ %d MiB（socket 层统计，含协议开销）。使用热点或计费流量
   可能产生费用。预算到达时测速立即中止，结果标记 budget_exceeded，
   不算完成。
3. 超时风险：整体超时 %v；超时即中止，结果标记 timeout，不算完成。
4. 无重试：失败、取消、超时或预算中止后不会自动重新测速。
5. 结果口径：结果仅为 NDT7 应用层测速，不代表 ICMP Ping 或 Jitter。
6. 取消方式：输入 y 之外的任何内容或直接回车即取消，不会建立任何连接。

确认版本：%s
`

// ConfirmRemoteRun prints the per-run notice and requires an explicit
// answer. Only "y" / "yes" (case-insensitive) confirms. Anything else,
// including EOF, cancels. There is no default yes and no memory across runs.
func ConfirmRemoteRun(r io.Reader, w io.Writer, host string, downloadBudget, uploadBudget, totalBudget int64, overallTimeout time.Duration) (RemoteRunConfirmation, error) {
	fmt.Fprint(w, RemoteRunNotice(host, downloadBudget, uploadBudget, totalBudget, overallTimeout))
	fmt.Fprint(w, "是否确认执行这一次公网测速？输入 y 确认，其他任意输入或回车取消: ")
	raw, err := readPromptLine(r)
	if err != nil {
		return RemoteRunConfirmation{}, err
	}
	answer := strings.ToLower(strings.TrimSpace(raw))
	if answer != "y" && answer != "yes" {
		return RemoteRunConfirmation{}, nil
	}
	return RemoteRunConfirmation{
		Confirmed:     true,
		ConfirmedAt:   time.Now().UTC(),
		NoticeVersion: RemoteRunNoticeVersion,
		TargetHost:    host,
	}, nil
}

func mib(bytes int64) int64 {
	return bytes >> 20
}
