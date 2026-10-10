package mlabpoc

import (
	"errors"
	"fmt"
	"html/template"
	"io"
	"strconv"
	"strings"
	"time"
)

// ConsentNoticeVersion identifies the exact consent wording shown to the
// user. The version is stored with every consent record so that a stored
// agreement can always be traced back to what was displayed.
//
// notice-2 (P0-K) adds the Locate discovery step and the planned traffic
// budget disclosures required before the first public measurement.
const ConsentNoticeVersion = "gospeed-mlab-notice-2"

const mlabPrivacyURL = "https://www.measurementlab.net/privacy/"
const mlabAUPURL = "https://www.measurementlab.net/aup/"

// consentNoticeText is the full disclosure that must be shown before any
// Locate query or NDT7 measurement connection is created. Every claim here
// mirrors the public M-Lab documentation as of the P0-K phase.
var consentNoticeText = `M-Lab 网络测速实验隐私告知（GoSpeed P0-K 实验模块）

1. 第三方基础设施：本次测速将连接 Measurement Lab（M-Lab）的第三方测速
   服务器。GoSpeed 不控制该基础设施，测试数据由 M-Lab 收集和发布。

2. 公网 IP 公开：M-Lab 会收集你当前网络的公网 IP 地址，并将它与测量数据
   一起公开发布。公开数据中不包含你的浏览记录等私人上网活动。

3. 长期保留：测试时间、公网 IP 和网络测量数据（下载/上传速率、TCP 指标、
   客户端名称与版本等）会被 M-Lab 长期保留，实际上没有删除期限，也没有
   针对单次测试的豁免机制。

4. Locate 节点查询：同意后 GoSpeed 会先向 M-Lab Locate 服务（HTTPS）查询
   附近测速节点，然后才会连接节点执行测速。未同意时连这一步也不会发生。

5. 计划流量预算（实验性保护）：下载 ≤ ` + mibLabel(DefaultDownloadBudgetBytes) + ` MiB、上传 ≤ ` + mibLabel(DefaultUploadBudgetBytes) + ` MiB、
   单次总计 ≤ ` + mibLabel(DefaultTotalBudgetBytes) + ` MiB，在 socket 层统计（含协议开销）。预算到达时测速
   立即中止，结果标记 budget_exceeded，不算完成。这是实验保护值，
   不是运营商计费口径，也不是测速目标。

6. 超时与重试：单方向整体超时 ` + DefaultOverallTimeout.String() + `。失败、取消、超时或预算中止后
   不会自动重新测速；GoSpeed 不会在后台自动测速。

7. 流量消耗：使用移动热点或计费流量时，上述流量可能消耗大量额度并产生
   费用。

8. 退出方式：你可以随时拒绝或取消。拒绝后 GoSpeed 不会建立任何测速连接，
   也不会执行 Locate 查询。

M-Lab 官方隐私政策：` + mlabPrivacyURL + `
M-Lab 可接受使用政策（AUP）：` + mlabAUPURL

func mibLabel(bytes int64) string { return strconv.FormatInt(bytes>>20, 10) }

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

// readPromptLine reads exactly one line from r, byte by byte, so that no
// input beyond the line is consumed from a shared reader. Excess piped input
// stays available for the next prompt. (The previous bufio.Scanner wrappers
// each swallowed the whole pipe buffer, which starved the second prompt of
// its answer and turned an explicit confirmation into an implicit cancel.)
func readPromptLine(r io.Reader) (string, error) {
	var line []byte
	one := make([]byte, 1)
	for {
		n, err := r.Read(one)
		if n > 0 {
			if one[0] == '\n' {
				return strings.TrimSuffix(string(line), "\r"), nil
			}
			line = append(line, one[0])
			if len(line) > 4096 {
				return "", errors.New("prompt input line exceeds 4096 bytes")
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return strings.TrimSuffix(string(line), "\r"), nil
			}
			return "", err
		}
	}
}

func PromptConsent(r io.Reader, w io.Writer) (ConsentRecord, error) {
	record := NewConsentRecord(defaultClientName, defaultClientVersion)
	fmt.Fprintln(w, consentNoticeText)
	fmt.Fprint(w, "是否同意将测速数据（含公网 IP）公开给 M-Lab？输入 y 同意，其他任意输入或回车取消: ")
	raw, err := readPromptLine(r)
	if err != nil {
		return record, err
	}
	answer := strings.ToLower(strings.TrimSpace(raw))
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
  <li><strong>Locate 节点查询</strong>：同意后会先向 M-Lab Locate 服务（HTTPS）查询附近节点；未同意时连这一步也不会发生。</li>
  <li><strong>计划流量预算（实验性保护）</strong>：下载 ≤ {{.DownloadBudgetMiB}} MiB、上传 ≤ {{.UploadBudgetMiB}} MiB、单次总计 ≤ {{.TotalBudgetMiB}} MiB（socket 层统计，含协议开销）。预算到达即中止（budget_exceeded，不算完成）。这是实验保护值，不是运营商计费口径。</li>
  <li><strong>超时与重试</strong>：单方向整体超时 {{.OverallTimeout}}；失败、取消、超时或预算中止后不会自动重新测速。</li>
  <li><strong>流量消耗</strong>：使用移动热点或计费流量时可能产生大量费用。</li>
  <li><strong>退出方式</strong>：拒绝或取消后不会建立任何测速连接，也不会执行 Locate 查询；GoSpeed 不会后台自动测速。</li>
</ol>
<p>
  M-Lab 官方<a href="{{.PrivacyURL}}" rel="noopener noreferrer" target="_blank">隐私政策</a>
  与 <a href="{{.AUPURL}}" rel="noopener noreferrer" target="_blank">可接受使用政策（AUP）</a>。
</p>
<form method="post" action="/consent">
  <label>
    <input type="checkbox" name="consent" value="agreed">
    我已阅读并同意：本次测速数据（包含公网 IP）将被 M-Lab 收集、公开并长期保留，并知悉计划流量预算与超时限制。
  </label>
  <br>
  <button type="submit" disabled>开始测试（勾选后由服务端校验）</button>
  <a href="/cancel">取消</a>
</form>
<p>提示：默认不同意。未勾选并提交前，不会发起 Locate 查询，也不会建立任何测速连接。</p>
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
		PrivacyURL        string
		AUPURL            string
		DownloadBudgetMiB string
		UploadBudgetMiB   string
		TotalBudgetMiB    string
		OverallTimeout    string
	}{
		PrivacyURL:        mlabPrivacyURL,
		AUPURL:            mlabAUPURL,
		DownloadBudgetMiB: mibLabel(DefaultDownloadBudgetBytes),
		UploadBudgetMiB:   mibLabel(DefaultUploadBudgetBytes),
		TotalBudgetMiB:    mibLabel(DefaultTotalBudgetBytes),
		OverallTimeout:    DefaultOverallTimeout.String(),
	}
	if err := tmpl.Execute(&builder, data); err != nil {
		return "", err
	}
	return builder.String(), nil
}
