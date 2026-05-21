package config

import (
	"strings"
	"time"
)

func (p PackageProbeConfig) EffectiveReachabilityTimeout() time.Duration {
	return durationOrDefault(p.ReachabilityTimeout, DefaultPackageProbeReachabilityTimeout)
}

func (p PackageProbeConfig) EffectiveArtifactTimeout() time.Duration {
	return durationOrDefault(p.ArtifactTimeout, DefaultPackageProbeArtifactTimeout)
}

func durationOrDefault(raw string, fallback string) time.Duration {
	if duration, err := time.ParseDuration(strings.TrimSpace(raw)); err == nil && duration > 0 {
		return duration
	}
	duration, err := time.ParseDuration(fallback)
	if err == nil && duration > 0 {
		return duration
	}
	return 0
}
