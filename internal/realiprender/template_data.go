package realiprender

import (
	"lanpanel/internal/realip"
	"lanpanel/internal/realipassets"
	"strings"
)

type TemplateData struct {
	ProfileName     string
	Provider        string
	RefreshInterval string
	TrustedCIDRs    []string
	LanpanelBinary  string
	StateJSON       string
	ProfileJSON     string
	ReferenceJSON   string
}

func NewTemplateData(profile realip.ProfileConfig, state realip.State, reference realip.Reference) (TemplateData, error) {
	marker := "Lanpanel-managed: realip.profile=" + strings.TrimSpace(profile.Name) + " provider=" + strings.TrimSpace(profile.Provider)
	stateJSON, err := marshalManagedJSON(struct {
		LanpanelManaged string `json:"lanpanel_managed"`
		realip.State
	}{LanpanelManaged: marker, State: state})
	if err != nil {
		return TemplateData{}, err
	}
	profileJSON, err := marshalManagedJSON(struct {
		LanpanelManaged string `json:"lanpanel_managed"`
		realip.ProfileConfig
	}{LanpanelManaged: marker, ProfileConfig: profile})
	if err != nil {
		return TemplateData{}, err
	}
	referenceJSON := ""
	if strings.TrimSpace(reference.AppName) != "" {
		referenceBytes, err := marshalManagedJSON(struct {
			LanpanelManaged string `json:"lanpanel_managed"`
			realip.Reference
		}{LanpanelManaged: marker, Reference: reference})
		if err != nil {
			return TemplateData{}, err
		}
		referenceJSON = string(referenceBytes)
	}
	refreshInterval := strings.TrimSpace(profile.RefreshInterval)
	if refreshInterval == "" {
		refreshInterval = "72h"
	}
	systemdRefreshInterval, err := realip.SystemdRefreshInterval(refreshInterval)
	if err != nil {
		return TemplateData{}, err
	}
	return TemplateData{
		ProfileName:     strings.TrimSpace(profile.Name),
		Provider:        strings.TrimSpace(profile.Provider),
		RefreshInterval: systemdRefreshInterval,
		TrustedCIDRs:    append([]string(nil), state.TrustedCIDRs...),
		LanpanelBinary:  realipassets.DefaultRefreshBinaryPath,
		StateJSON:       string(stateJSON),
		ProfileJSON:     string(profileJSON),
		ReferenceJSON:   referenceJSON,
	}, nil
}
