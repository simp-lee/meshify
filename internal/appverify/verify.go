package appverify

import (
	"fmt"
	"meshify/internal/appassets"
	"meshify/internal/appconfig"
	"meshify/internal/apprender"
	"meshify/internal/components/appsvc"
	"strings"
)

type Status string

const (
	StatusPass Status = "pass"
	StatusFail Status = "fail"
)

type Check struct {
	ID      string
	Status  Status
	Summary string
}

type Report struct {
	Checks []Check
}

func StaticReport(cfg appconfig.Config, staged []apprender.StagedFile) Report {
	checks := []Check{}
	add := func(id string, status Status, summary string) {
		checks = append(checks, Check{ID: id, Status: status, Summary: summary})
	}

	if err := cfg.Validate(); err != nil {
		add("config", StatusFail, "app 配置校验失败: "+err.Error())
		return Report{Checks: checks}
	}
	add("config", StatusPass, "app 配置 schema 和静态字段有效")
	if cfg.RequiresTailscale() {
		add("tailscale", StatusPass, "app 配置推导需要 Tailscale client 前置条件")
	} else {
		add("tailscale", StatusPass, "app 配置推导不需要 Tailscale client 前置条件")
	}

	names, err := appsvc.NewNames(cfg)
	if err != nil {
		add("names", StatusFail, err.Error())
	} else {
		add("names", StatusPass, "app 资源命名和路径派生有效")
	}

	if len(staged) == 0 {
		add("templates", StatusFail, "未渲染任何 app runtime 模板")
		return Report{Checks: checks}
	}

	hasNginx := false
	for _, file := range staged {
		text := string(file.Content)
		if stale, label := containsLegacyLegoV4Form(text); stale {
			add("lego-v5", StatusFail, "渲染文件包含 lego v4 语法: "+label)
			return Report{Checks: checks}
		}
		if sensitive, label := containsSensitiveValue(text); sensitive {
			add("secrets", StatusFail, "渲染文件包含疑似敏感值: "+label)
			return Report{Checks: checks}
		}
		if err := appsvc.CheckManagedContent(cfg.App.Name, file.Content); err != nil {
			add("ownership", StatusFail, file.HostPath+" "+err.Error())
			return Report{Checks: checks}
		}
		if file.SourcePath == "templates/app/nginx.conf.tmpl" {
			hasNginx = true
			if err == nil {
				if validateErr := appsvc.ValidateRenderedNginx(cfg, names, file.Content); validateErr != nil {
					add("nginx", StatusFail, validateErr.Error())
					return Report{Checks: checks}
				}
			}
		}
	}
	add("ownership", StatusPass, "渲染文件包含当前 app 的 Meshify-managed marker")
	add("secrets", StatusPass, "渲染文件未包含 Tailscale auth key 或 DNS token")
	add("lego-v5", StatusPass, "渲染文件未包含 lego v4 renew 或旧 hook 语法")
	if err := validateStagedRuntimeSet(cfg, staged); err != nil {
		add("templates", StatusFail, err.Error())
		return Report{Checks: checks}
	}
	add("templates", StatusPass, fmt.Sprintf("已渲染 %d 个 app runtime 文件，且 systemd、Nginx、deploy hook 计划完整", len(staged)))
	if hasNginx {
		add("nginx", StatusPass, "Nginx app 站点包含多域名、Host/SNI guard、WebSocket 和固定 upstream")
	} else {
		add("nginx", StatusFail, "缺少 Nginx app 站点模板")
	}

	return Report{Checks: checks}
}

func containsLegacyLegoV4Form(text string) (bool, string) {
	for _, stale := range []string{" renew --renew-hook", "--run-hook", "--renew-hook", "LEGO_CERT_", "LEGO_ACCOUNT_EMAIL"} {
		if strings.Contains(text, stale) {
			return true, stale
		}
	}
	return false, ""
}

func validateStagedRuntimeSet(cfg appconfig.Config, staged []apprender.StagedFile) error {
	expected, err := appassets.RuntimeCatalog(cfg)
	if err != nil {
		return err
	}
	expectedBySource := make(map[string]appassets.Asset, len(expected))
	for _, asset := range expected {
		expectedBySource[asset.SourcePath] = asset
	}
	seen := make(map[string]struct{}, len(staged))
	for _, file := range staged {
		asset, ok := expectedBySource[file.SourcePath]
		if !ok {
			return fmt.Errorf("渲染了未预期的 app runtime 文件: %s", file.SourcePath)
		}
		if _, exists := seen[file.SourcePath]; exists {
			return fmt.Errorf("重复渲染 app runtime 文件: %s", file.SourcePath)
		}
		seen[file.SourcePath] = struct{}{}
		if strings.TrimSpace(file.HostPath) == "" {
			return fmt.Errorf("%s 缺少目标路径", file.SourcePath)
		}
		if file.HostPath != asset.HostPath {
			return fmt.Errorf("%s 目标路径与 runtime catalog 不一致: %s != %s", file.SourcePath, file.HostPath, asset.HostPath)
		}
		if file.ContentMode != asset.ContentMode || file.Mode != asset.Mode {
			return fmt.Errorf("%s 的渲染模式或文件权限与 runtime catalog 不一致", file.SourcePath)
		}
	}
	for _, asset := range expected {
		if _, ok := seen[asset.SourcePath]; !ok {
			return fmt.Errorf("缺少 app runtime 文件: %s", asset.SourcePath)
		}
	}
	return nil
}

func (report Report) FailedCount() int {
	count := 0
	for _, check := range report.Checks {
		if check.Status == StatusFail {
			count++
		}
	}
	return count
}

func (report Report) Summary() string {
	if report.FailedCount() > 0 {
		return fmt.Sprintf("app verify 发现 %d 个失败检查", report.FailedCount())
	}
	return "app 静态检查通过"
}

func SummarizeChecks(checks []Check) string {
	parts := make([]string, 0, len(checks))
	for _, check := range checks {
		parts = append(parts, check.ID+"="+string(check.Status))
	}
	return strings.Join(parts, ", ")
}

func containsSensitiveValue(text string) (bool, string) {
	lower := strings.ToLower(text)
	for _, prefix := range []string{"authkey-", "tskey-auth-", "hskey-auth-"} {
		if strings.Contains(lower, prefix) {
			return true, prefix
		}
	}
	for _, line := range strings.Split(text, "\n") {
		key, ok := envAssignmentKey(line)
		if !ok {
			continue
		}
		if isRawCredentialKey(key) {
			return true, key
		}
	}
	return false, ""
}

func envAssignmentKey(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
		return "", false
	}
	line = strings.TrimPrefix(line, "export ")
	line = strings.TrimPrefix(line, "Environment=")
	key, _, ok := strings.Cut(line, "=")
	if !ok {
		return "", false
	}
	key = strings.ToUpper(strings.TrimSpace(key))
	return key, key != ""
}

func isRawCredentialKey(key string) bool {
	if strings.HasSuffix(key, "_FILE") {
		return false
	}
	return strings.Contains(key, "TOKEN") ||
		strings.Contains(key, "SECRET") ||
		strings.Contains(key, "API_KEY") ||
		key == "AWS_ACCESS_KEY_ID"
}
