package main

import (
	"strings"
	"testing"

	"github.com/AbsoluteZero001/GoSpeed/internal/nodes"
	"github.com/AbsoluteZero001/GoSpeed/internal/speedtest"
)

// noticeText joins every visible field so tests can assert on what a user
// actually reads.
func noticeText(notice MeasurementNotice) string {
	return strings.Join([]string{notice.Title, notice.Message, notice.Disclaimer, notice.TransferLabel}, " | ")
}

func TestNoticeForScopeSeparatesLoopbackLANAndPublic(t *testing.T) {
	cases := []struct {
		name        string
		scope       string
		wantKind    string
		wantTitle   string
		wantLabel   string
		wantPhrases []string
	}{
		{
			name:      "loopback",
			scope:     nodes.ScopeLocal,
			wantKind:  NoticeLoopback,
			wantTitle: "本机回环性能测试",
			wantLabel: "本机吞吐量",
			wantPhrases: []string{
				"本机回环",
				"不代表真实宽带速度",
			},
		},
		{
			name:      "lan",
			scope:     nodes.ScopeLAN,
			wantKind:  NoticeLAN,
			wantTitle: "局域网测速",
			wantLabel: "局域网吞吐量",
			wantPhrases: []string{
				"局域网测速，不代表互联网宽带速度",
			},
		},
		{
			name:      "public",
			scope:     nodes.ScopeRemote,
			wantKind:  NoticePublic,
			wantTitle: "公网目标测速",
			wantLabel: "实测速率",
			wantPhrases: []string{
				"结果受到服务器带宽、路由、网络拥塞和测速配置影响",
				"地址分类不能证明流量经过了哪些网络",
			},
		},
		{
			name:      "unknown host name",
			scope:     nodes.ScopeUnknown,
			wantKind:  NoticeUnknownPath,
			wantTitle: "目标路径未知",
			wantLabel: "实测速率",
			wantPhrases: []string{
				"无法仅凭地址判断是否经过公网",
				"实际可达路径",
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			notice := noticeForScope(testCase.scope)
			if notice.Kind != testCase.wantKind {
				t.Fatalf("kind = %q, want %q", notice.Kind, testCase.wantKind)
			}
			if notice.Title != testCase.wantTitle {
				t.Fatalf("title = %q, want %q", notice.Title, testCase.wantTitle)
			}
			if notice.TransferLabel != testCase.wantLabel {
				t.Fatalf("transfer label = %q, want %q", notice.TransferLabel, testCase.wantLabel)
			}
			text := noticeText(notice)
			for _, phrase := range testCase.wantPhrases {
				if !strings.Contains(text, phrase) {
					t.Fatalf("notice %q does not contain %q", text, phrase)
				}
			}
		})
	}
}

// A loopback target must never be described with public-internet wording, and
// the notice must say the numbers are local throughput, not broadband speed.
func TestLoopbackNoticeDoesNotClaimBroadband(t *testing.T) {
	notice := noticeForScope(nodes.ScopeLocal)
	text := noticeText(notice)
	if !strings.Contains(text, "本机吞吐量") {
		t.Fatalf("loopback notice does not label the metrics as local throughput: %q", text)
	}
	if !strings.Contains(text, "不代表真实宽带速度") {
		t.Fatalf("loopback notice does not warn about broadband speed: %q", text)
	}
	for _, forbidden := range []string{"已经测得", "真实宽带速度为", "公网目标测速"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("loopback notice contains an over-claim %q: %q", forbidden, text)
		}
	}
}

func TestNoticeForTargetPrefersLoopback(t *testing.T) {
	cases := []struct {
		name   string
		target speedtest.Target
	}{
		{
			name:   "loopback address literal",
			target: speedtest.Target{ID: "local", BaseURL: "http://127.0.0.1:8080", Protocol: speedtest.ProtocolHTTP},
		},
		{
			name:   "ipv6 loopback literal",
			target: speedtest.Target{ID: "local", BaseURL: "http://[::1]:8080", Protocol: speedtest.ProtocolHTTP},
		},
		{
			name:   "localhost name",
			target: speedtest.Target{ID: "local", BaseURL: "http://localhost:8080", Protocol: speedtest.ProtocolHTTP},
		},
		{
			name:   "explicit local flag wins",
			target: speedtest.Target{ID: "local", BaseURL: "https://speed.example.com", Protocol: speedtest.ProtocolHTTPS, Local: true},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			notice := noticeForTarget(testCase.target)
			if notice.Kind != NoticeLoopback {
				t.Fatalf("kind = %q, want %q (notice: %s)", notice.Kind, NoticeLoopback, noticeText(notice))
			}
		})
	}
}

func TestNoticeForTargetKeepsHostNamesUnknown(t *testing.T) {
	// A host name may resolve into a LAN, a VPN or the public internet; the
	// notice must not claim "public" from the string alone.
	notice := noticeForTarget(speedtest.Target{
		ID:       "remote",
		BaseURL:  "https://speed.example.com",
		Protocol: speedtest.ProtocolHTTPS,
	})
	if notice.Kind != NoticeUnknownPath {
		t.Fatalf("kind = %q, want %q", notice.Kind, NoticeUnknownPath)
	}
	if !strings.Contains(noticeText(notice), "无法仅凭地址判断") {
		t.Fatalf("host name notice must state the path is unknown: %q", noticeText(notice))
	}
}

func TestNodeSetNoticeOnlyWarnsWhenNothingBeyondLoopback(t *testing.T) {
	local := nodes.LocalDefault()
	lan := nodes.Node{
		ID:       "lan",
		Name:     "LAN server",
		BaseURL:  "http://192.168.1.10:8080",
		Protocol: nodes.ProtocolHTTP,
		Enabled:  true,
	}
	disabledLAN := lan
	disabledLAN.Enabled = false

	cases := []struct {
		name        string
		list        []nodes.Node
		wantKind    string
		wantPhrases []string
	}{
		{
			name:     "only the built-in loopback node",
			list:     []nodes.Node{local},
			wantKind: NoticeLoopbackOnly,
			wantPhrases: []string{
				"本机回环",
				"还没有测得真实宽带速度",
				"本机吞吐量",
			},
		},
		{
			name:     "loopback plus disabled LAN node",
			list:     []nodes.Node{local, disabledLAN},
			wantKind: NoticeLoopbackOnly,
			wantPhrases: []string{
				"还没有测得真实宽带速度",
			},
		},
		{
			name:     "no nodes at all",
			list:     nil,
			wantKind: NoticeNoNodes,
			wantPhrases: []string{
				"没有可用的测速节点",
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			notice := nodeSetNotice(testCase.list)
			if notice == nil {
				t.Fatal("expected a notice")
			}
			if notice.Kind != testCase.wantKind {
				t.Fatalf("kind = %q, want %q", notice.Kind, testCase.wantKind)
			}
			text := noticeText(*notice)
			for _, phrase := range testCase.wantPhrases {
				if !strings.Contains(text, phrase) {
					t.Fatalf("notice %q does not contain %q", text, phrase)
				}
			}
		})
	}

	// An enabled non-loopback node means a real broadband measurement is
	// possible, so no "only local" warning is shown.
	if notice := nodeSetNotice([]nodes.Node{local, lan}); notice != nil {
		t.Fatalf("unexpected notice when a LAN node is enabled: %s", noticeText(*notice))
	}
	if notice := nodeSetNotice([]nodes.Node{disabledLAN}); notice != nil && notice.Kind == NoticeLoopbackOnly {
		t.Fatalf("disabled nodes must not count as loopback-only nodes: %s", noticeText(*notice))
	}
}
