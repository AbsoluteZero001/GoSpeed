package main

import (
	"github.com/AbsoluteZero001/GoSpeed/internal/nodes"
	"github.com/AbsoluteZero001/GoSpeed/internal/speedtest"
)

// Notice kinds. They are presentation buckets derived from the configured
// address; they are not claims about the path the traffic actually took.
const (
	NoticeLoopback     = "loopback"
	NoticeLAN          = "lan"
	NoticePublic       = "public"
	NoticeUnknownPath  = "unknown"
	NoticeLoopbackOnly = "loopback-only"
	NoticeNoNodes      = "no-nodes"
)

// Transfer labels used to mark rate metrics for what they actually measured.
const (
	labelLocalThroughput = "本机吞吐量"
	labelLANThroughput   = "局域网吞吐量"
	labelMeasuredRate    = "实测速率"
)

// MeasurementNotice explains how the numbers of a run may be described. The
// wording lives in the backend so the GUI (and any later frontend) shows the
// same, testable text.
type MeasurementNotice struct {
	// Kind is one of the Notice* constants.
	Kind string `json:"kind"`
	// Scope is the raw address classification (local / lan / remote / unknown).
	Scope string `json:"scope,omitempty"`
	Title string `json:"title"`
	// Message states what was measured; it never claims a network path that the
	// classification alone cannot prove.
	Message string `json:"message"`
	// Disclaimer is the caution that must be shown next to the numbers.
	Disclaimer string `json:"disclaimer,omitempty"`
	// TransferLabel qualifies the rate metrics, for example "本机吞吐量".
	TransferLabel string `json:"transferLabel,omitempty"`
}

// noticeForScope maps an address classification onto the presentation notice.
// A host name stays "unknown": DNS could resolve into a LAN, a VPN, a proxy or
// the public internet, so the notice must not upgrade it to "public".
func noticeForScope(scope string) MeasurementNotice {
	switch scope {
	case nodes.ScopeLocal:
		return MeasurementNotice{
			Kind:          NoticeLoopback,
			Scope:         scope,
			Title:         "本机回环性能测试",
			Message:       "目标地址是本机回环地址（127.0.0.1 / ::1 / localhost）：流量不经过网卡与互联网。",
			Disclaimer:    "结果仅代表本机回环吞吐量，不代表真实宽带速度。",
			TransferLabel: labelLocalThroughput,
		}
	case nodes.ScopeLAN:
		return MeasurementNotice{
			Kind:          NoticeLAN,
			Scope:         scope,
			Title:         "局域网测速",
			Message:       "目标地址是局域网地址：流量经过本机网卡与局域网链路。",
			Disclaimer:    "局域网测速，不代表互联网宽带速度。",
			TransferLabel: labelLANThroughput,
		}
	case nodes.ScopeRemote:
		return MeasurementNotice{
			Kind:          NoticePublic,
			Scope:         scope,
			Title:         "公网目标测速",
			Message:       "目标地址是公网 IP 字面量；结果受到服务器带宽、路由、网络拥塞和测速配置影响。",
			Disclaimer:    "实际路径仍由路由与中间网络决定，地址分类不能证明流量经过了哪些网络。",
			TransferLabel: labelMeasuredRate,
		}
	default:
		return MeasurementNotice{
			Kind:          NoticeUnknownPath,
			Scope:         nodes.ScopeUnknown,
			Title:         "目标路径未知",
			Message:       "目标是域名或无法分类的地址：GoSpeed 不做 DNS 解析猜测，无法仅凭地址判断是否经过公网。",
			Disclaimer:    "结果只代表到该目标的实际可达路径，并受到服务器带宽、路由、网络拥塞和测速配置影响。",
			TransferLabel: labelMeasuredRate,
		}
	}
}

// noticeForTarget classifies a resolved measurement target. An explicitly
// loopback target wins over the URL classification so the strongest, always
// accurate statement is shown.
func noticeForTarget(target speedtest.Target) MeasurementNotice {
	if target.Local || nodes.IsLoopbackHost(urlHost(target.BaseURL)) {
		return noticeForScope(nodes.ScopeLocal)
	}
	return noticeForScope(nodes.NetworkScope(target.BaseURL))
}

// nodeSetNotice describes the configured nodes. It is only set when the user
// cannot currently measure anything beyond the local machine, so the UI never
// implies that a real broadband speed was measured.
func nodeSetNotice(list []nodes.Node) *MeasurementNotice {
	enabled := 0
	loopbackOnly := true
	for _, node := range list {
		if !node.Enabled {
			continue
		}
		enabled++
		if !isLoopbackNode(node) {
			loopbackOnly = false
		}
	}
	switch {
	case enabled == 0:
		return &MeasurementNotice{
			Kind:    NoticeNoNodes,
			Title:   "没有已启用的测速节点",
			Message: "当前没有可用的测速节点：请启用或添加一个节点后再开始测速。",
		}
	case loopbackOnly:
		return &MeasurementNotice{
			Kind:          NoticeLoopbackOnly,
			Scope:         nodes.ScopeLocal,
			Title:         "当前仅配置本机回环节点",
			Message:       "已启用节点全部指向本机回环地址，因此只能测量本机吞吐量，还没有测得真实宽带速度。",
			Disclaimer:    "如需测量宽带，请添加并启用局域网或公网测速节点（gospeed nodes add / enable）。",
			TransferLabel: labelLocalThroughput,
		}
	default:
		// At least one enabled node is not loopback: no automatic notice. The
		// target notice still describes the node that a run actually used.
		return nil
	}
}

// isLoopbackNode reports whether a configured node points at this machine.
// Loopback nodes must be marked local by the address policy (internal/nodes
// ValidateTarget), but the address is classified as well so a hand built list
// cannot slip through.
func isLoopbackNode(node nodes.Node) bool {
	if node.Local {
		return true
	}
	return nodes.IsLoopbackHost(urlHost(node.BaseURL))
}
