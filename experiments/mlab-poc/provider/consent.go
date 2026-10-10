package mlabpoc

import (
	"bufio"
	"fmt"
	"html/template"
	"io"
	"strings"
	"time"
)

// ConsentNoticeVersion identifies the exact consent wording shown to the
// user. The version is stored with every consent record so that a stored
// agreement can always be traced back to what was displayed.
const ConsentNoticeVersion = "gospeed-mlab-notice-1"

const mlabPrivacyURL = "https://www.measurementlab.net/privacy/"
const mlabAUPURL = "https://www.measurementlab.net/aup/"

// consentNoticeText is the full disclosure that must be shown before any
// NDT7 measurement connection is created. Every claim here mirrors the
// public M-Lab documentation as of the P0-J phase.
const consentNoticeText = `M-Lab 网络测速实验隐私告知（GoSpeed P0-J 实验模块）

1. 第三方基础设施：本次测速将连接 Measurement Lab（M-Lab）的第三方测速
   服务器。GoSpeed 不控制该基础设施，测试数据由 M-Lab 收集和发布。

2. 公网 IP 公开：M-Lab 会收集你当前网络的公网 IP 地址，并将它与测量数据
   一起公开发布。公开数据中不包含你的浏览记录等私人上网活动。

3. 长期保留：测试时间、公网 IP 和网络测量数据（下载/上传速率、TCP 指标、
   客户端名称与版本等）会被 M-Lab 长期保留，实际上没有删除期限，也没有
   针对单次测试的豁免机制。

4. 流量消耗：一次 NDT7 测速会传输数十 MiB 级别的数据。使用移动热点或
   计费流量时，这可能消耗大量流量并产生费用。

5. 退出方式：你可以随时拒绝或取消。拒绝后 GoSpeed 不会建立任何测速连接。
   GoSpeed 不会在后台自动执行测速，也不会自动重复测速。

M-Lab 官方隐私政策：` + mlabPrivacyURL + `
M-Lab 可接受使用政策（AUP）：` + mlabAUPURL

// DefaultConsentNotice returns the full consent disclosure text.
func DefaultConsentNotice() string {
	return consentNoticeText
}

// ConsentRecord captures an explicit consent decision. The zero value means
// "not agreed": consent is opt-in, never pre-checked and never inferred.
type ConsentRecord struct {
	Agreed        bool      `json:"agreed"`
	AgreedAt      time.Time `json:"agreedAt,omitempty"`
	PolicyVersion string    `json:"policyVersion,omitempty"`
	NoticeText    string    `json:"noticeText,omitempty"`
	ClientName    string    `json:"clientName,omitempty"`
	ClientVersion string    `json:"clientVersion,omitempty"`
}

// NewConsentRecord returns a record with Agreed=false. It only becomes valid
// through an explicit user action (e.g. PromptConsent or an explicit UI
// checkbox interaction).
func NewConsentRecord(clientName, clientVersion string) ConsentRecord {
	return ConsentRecord{
		Agreed:        false,
		PolicyVersion: ConsentNoticeVersion,
		NoticeText:    consentNoticeText,
		ClientName:    clientName,
		ClientVersion: clientVersion,
	}
}

// PromptConsent prints the notice and requires an explicit answer. Only the
// literal inputs "y" or "yes" (case-insensitive) count as agreement. Empty
// input, "n", "no", any other answer, or EOF means NOT agreed. There is no
// default yes.
func PromptConsent(r io.Reader, w io.Writer) (ConsentRecord, error) {
	record := NewConsentRecord(defaultClientName, defaultClientVersion)
	fmt.Fprintln(w, consentNoticeText)
	fmt.Fprint(w, "是否同意将测速数据（含公网 IP）公开给 M-Lab？输入 y 同意，其他任意输入或回车取消: ")
	reader := bufio.NewScanner(r)
	if !reader.Scan() {
		if err := reader.Err(); err != nil {
			return record, err
		}
		return record, nil // EOF = no consent
	}
	answer := strings.ToLower(strings.TrimSpace(reader.Text()))
	if answer == "y" || answer == "yes" {
		record.Agreed = true
		record.AgreedAt = time.Now().UTC()
	}
	return record, nil
}

const consentPageTemplate = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<title>GoSpeed M-Lab 测速隐私同意（实验）</title>
</head>
<body>
<h1>M-Lab 网络测速隐私同意（GoSpeed 实验模块）</h1>
<ol>
  <li><strong>第三方基础设施</strong>：测速将连接 Measurement Lab（M-Lab）的第三方服务器，数据由 M-Lab 收集和发布。</li>
  <li><strong>公网 IP 公开</strong>：你的公网 IP 地址会与测量数据一起被 M-Lab 公开发布。</li>
  <li><strong>长期保留</strong>：测试时间、公网 IP 和网络测量数据会被长期保留，没有删除期限，也没有单次测试豁免机制。</li>
  <li><strong>流量消耗</strong>：一次测速可能传输数十 MiB 数据。使用移动热点或计费流量时可能产生大量费用。</li>
  <li><strong>退出方式</strong>：拒绝或取消后不会建立任何测速连接；GoSpeed 不会后台自动测速，也不会自动重复测速。</li>
</ol>
<p>
  M-Lab 官方<a href="{{.PrivacyURL}}" rel="noopener noreferrer" target="_blank">隐私政策</a>
  与 <a href="{{.AUPURL}}" rel="noopener noreferrer" target="_blank">可接受使用政策（AUP）</a>。
</p>
<form method="post" action="/consent">
  <label>
    <input type="checkbox" name="consent" value="agreed">
    我已阅读并同意：本次测速数据（包含公网 IP）将被 M-Lab 收集、公开并长期保留。
  </label>
  <br>
  <button type="submit" disabled>开始测试（勾选后由服务端校验）</button>
  <a href="/cancel">取消</a>
</form>
<p>提示：默认不同意。未勾选并提交前，不会发起任何测速连接。</p>
</body>
</html>
`

// RenderConsentHTML renders the experimental localhost consent page. The
// checkbox is never pre-checked and the submit button starts disabled;
// real activation belongs to the future GUI integration and must always be
// validated server-side.
func RenderConsentHTML() (string, error) {
	tmpl, err := template.New("consent").Parse(consentPageTemplate)
	if err != nil {
		return "", err
	}
	var builder strings.Builder
	data := struct {
		PrivacyURL string
		AUPURL     string
	}{mlabPrivacyURL, mlabAUPURL}
	if err := tmpl.Execute(&builder, data); err != nil {
		return "", err
	}
	return builder.String(), nil
}
