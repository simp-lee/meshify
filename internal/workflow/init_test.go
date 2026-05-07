package workflow

import (
	"fmt"
	"meshify/internal/config"
	"meshify/internal/output"
	"strings"
	"testing"
)

type scriptedPrompter struct {
	enabled  bool
	texts    []string
	confirms []bool
	selects  []string
}

func (prompter *scriptedPrompter) Enabled() bool {
	return prompter.enabled
}

func (prompter *scriptedPrompter) Text(label string, prompt output.TextPrompt) (string, error) {
	if len(prompter.texts) == 0 {
		return "", fmt.Errorf("unexpected text prompt %q", label)
	}
	value := prompter.texts[0]
	prompter.texts = prompter.texts[1:]
	if strings.TrimSpace(value) == "" {
		value = prompt.Default
	}
	if prompt.Validate != nil {
		if err := prompt.Validate(value); err != nil {
			return "", err
		}
	}
	return value, nil
}

func (prompter *scriptedPrompter) Confirm(label string, prompt output.ConfirmPrompt) (bool, error) {
	if len(prompter.confirms) == 0 {
		return false, fmt.Errorf("unexpected confirm prompt %q", label)
	}
	value := prompter.confirms[0]
	prompter.confirms = prompter.confirms[1:]
	return value, nil
}

func (prompter *scriptedPrompter) Select(label string, prompt output.SelectPrompt) (string, error) {
	if len(prompter.selects) == 0 {
		return "", fmt.Errorf("unexpected select prompt %q", label)
	}
	value := prompter.selects[0]
	prompter.selects = prompter.selects[1:]
	if strings.TrimSpace(value) == "" {
		value = prompt.Default
	}
	for _, option := range prompt.Options {
		if option == value {
			return value, nil
		}
	}
	return "", fmt.Errorf("%s must be one of: %s", label, strings.Join(prompt.Options, ", "))
}

func TestRunInitCollectsDefaultGuidedConfig(t *testing.T) {
	t.Parallel()

	prompter := &scriptedPrompter{
		enabled:  true,
		confirms: []bool{false},
		texts: []string{
			"https://hs.example.com",
			"tailnet.example.com",
			"ops@example.com",
		},
	}

	result, err := RunInit(prompter, InitOptions{})
	if err != nil {
		returnError(t, err)
	}

	if result.Source != InitSourceGuided {
		t.Fatalf("Source = %q, want %q", result.Source, InitSourceGuided)
	}
	if result.Mode != InitModeDefault {
		t.Fatalf("Mode = %q, want %q", result.Mode, InitModeDefault)
	}
	if result.Config.Default.ACMEChallenge != config.ACMEChallengeHTTP01 {
		t.Fatalf("ACMEChallenge = %q, want %q", result.Config.Default.ACMEChallenge, config.ACMEChallengeHTTP01)
	}
	if result.Config.Advanced.PackageSource.Mode != config.PackageSourceModeDirect {
		t.Fatalf("PackageSource.Mode = %q, want %q", result.Config.Advanced.PackageSource.Mode, config.PackageSourceModeDirect)
	}

	response := result.Response("meshify.yaml")
	if response.Summary != "wrote guided config" {
		t.Fatalf("Summary = %q, want %q", response.Summary, "wrote guided config")
	}
	if response.Fields[1].Value != "guided default" {
		t.Fatalf("config source = %q, want %q", response.Fields[1].Value, "guided default")
	}
	assertContainsStep(t, response.NextSteps, "Review the generated default section")
	assertContainsStep(t, response.NextSteps, "meshify init --advanced --config meshify.advanced.yaml")
	assertNotContainsStep(t, response.NextSteps, "meshify init --advanced --config meshify.yaml")
	assertContainsStep(t, response.NextSteps, "meshify deploy --config meshify.yaml")
	assertContainsStep(t, response.NextSteps, "meshify verify --config meshify.yaml")
	assertContainsStep(t, response.NextSteps, "validate this config now")
}

func TestRunInitAllowsServerURLBeforeBaseDomainIsKnown(t *testing.T) {
	t.Parallel()

	prompter := &scriptedPrompter{
		enabled:  true,
		confirms: []bool{false},
		texts: []string{
			"https://tailnet.example.com",
			"mesh.example.com",
			"ops@example.com",
		},
	}

	result, err := RunInit(prompter, InitOptions{})
	if err != nil {
		returnError(t, err)
	}

	if result.Config.Default.ServerURL != "https://tailnet.example.com" {
		t.Fatalf("ServerURL = %q, want %q", result.Config.Default.ServerURL, "https://tailnet.example.com")
	}
	if result.Config.Default.BaseDomain != "mesh.example.com" {
		t.Fatalf("BaseDomain = %q, want %q", result.Config.Default.BaseDomain, "mesh.example.com")
	}
}

func TestExampleInitResultResponseUsesSeparateAdvancedConfigPath(t *testing.T) {
	t.Parallel()

	response := ExampleInitResult().Response("meshify.yaml")

	assertContainsStep(t, response.NextSteps, "meshify init --advanced --config meshify.advanced.yaml")
	assertNotContainsStep(t, response.NextSteps, "meshify init --advanced --config meshify.yaml")
	assertContainsStep(t, response.NextSteps, "meshify verify --config meshify.yaml")
	assertContainsStep(t, response.NextSteps, "validate this config now")
}

func TestRunInitCollectsAdvancedGuidedConfig(t *testing.T) {
	t.Parallel()

	prompter := &scriptedPrompter{
		enabled: true,
		texts: []string{
			"https://hs.example.com",
			"tailnet.example.com",
			"ops@example.com",
			"cloudflare",
			"/etc/meshify/dns01/cloudflare.env",
			config.DefaultHeadscaleVersion,
			"https://mirror.example.com/headscale.deb",
			strings.Repeat("a", 64),
			"http://proxy.internal:8080",
			"http://proxy.internal:8443",
			"127.0.0.1,localhost",
			"203.0.113.10",
			"2001:db8::10",
		},
		confirms: []bool{true, true},
		selects: []string{
			config.ACMEChallengeDNS01,
			config.PackageSourceModeMirror,
			config.ArchARM64,
		},
	}

	result, err := RunInit(prompter, InitOptions{Advanced: true})
	if err != nil {
		returnError(t, err)
	}

	if result.Mode != InitModeAdvanced {
		t.Fatalf("Mode = %q, want %q", result.Mode, InitModeAdvanced)
	}
	if result.Config.Default.ACMEChallenge != config.ACMEChallengeDNS01 {
		t.Fatalf("ACMEChallenge = %q, want %q", result.Config.Default.ACMEChallenge, config.ACMEChallengeDNS01)
	}
	if result.Config.Advanced.DNS01.Provider != "cloudflare" {
		t.Fatalf("DNS01.Provider = %q, want %q", result.Config.Advanced.DNS01.Provider, "cloudflare")
	}
	if result.Config.Advanced.DNS01.EnvFile != "/etc/meshify/dns01/cloudflare.env" {
		t.Fatalf("DNS01.EnvFile = %q, want env path", result.Config.Advanced.DNS01.EnvFile)
	}
	if result.Config.Advanced.PackageSource.Mode != config.PackageSourceModeMirror {
		t.Fatalf("PackageSource.Mode = %q, want %q", result.Config.Advanced.PackageSource.Mode, config.PackageSourceModeMirror)
	}
	if result.Config.Advanced.Proxy.HTTPProxy != "http://proxy.internal:8080" {
		t.Fatalf("HTTPProxy = %q, want %q", result.Config.Advanced.Proxy.HTTPProxy, "http://proxy.internal:8080")
	}
	if result.Config.Advanced.Platform.Arch != config.ArchARM64 {
		t.Fatalf("Platform.Arch = %q, want %q", result.Config.Advanced.Platform.Arch, config.ArchARM64)
	}
	if result.Config.Advanced.Network.PublicIPv6 != "2001:db8::10" {
		t.Fatalf("PublicIPv6 = %q, want %q", result.Config.Advanced.Network.PublicIPv6, "2001:db8::10")
	}

	response := result.Response("meshify.yaml")
	if response.Fields[1].Value != "guided advanced" {
		t.Fatalf("config source = %q, want %q", response.Fields[1].Value, "guided advanced")
	}
	assertContainsStep(t, response.NextSteps, "Review the generated advanced section before deploy")
	assertContainsStep(t, response.NextSteps, "Prepare the root-only DNS-01 env file")
	assertContainsStep(t, response.NextSteps, "meshify deploy --config meshify.yaml")
	assertContainsStep(t, response.NextSteps, "meshify verify --config meshify.yaml")
}

func TestRunInitReturnsPromptValidationError(t *testing.T) {
	t.Parallel()

	prompter := &scriptedPrompter{
		enabled:  true,
		confirms: []bool{false},
		texts: []string{
			"https://hs.example.com",
			"hs.example.com",
		},
	}

	_, err := RunInit(prompter, InitOptions{})
	if err == nil {
		t.Fatal("RunInit() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "default.base_domain must not equal default.server_url host") {
		t.Fatalf("error = %q, want base-domain validation", err.Error())
	}
}

func assertContainsStep(t *testing.T, steps []string, want string) {
	t.Helper()

	for _, step := range steps {
		if strings.Contains(step, want) {
			return
		}
	}

	t.Fatalf("steps = %#v, want substring %q", steps, want)
}

func assertNotContainsStep(t *testing.T, steps []string, unwanted string) {
	t.Helper()

	for _, step := range steps {
		if strings.Contains(step, unwanted) {
			t.Fatalf("steps = %#v, do not want substring %q", steps, unwanted)
		}
	}
}

func returnError(t *testing.T, err error) {
	t.Helper()
	t.Fatalf("RunInit() error = %v", err)
}
