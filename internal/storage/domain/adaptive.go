package domain

import "math"

// AdaptiveIDConfig holds configuration for adaptive ID length computation.
// Duplicated from internal/storage/issueops to keep the new domain layer
// independent of the legacy embedded code path. Both copies must agree.
type AdaptiveIDConfig struct {
	MaxCollisionProbability float64
	MinLength               int
	MaxLength               int
}

// DefaultAdaptiveConfig returns the default adaptive ID configuration.
func DefaultAdaptiveConfig() AdaptiveIDConfig {
	return AdaptiveIDConfig{
		MaxCollisionProbability: 0.25,
		MinLength:               3,
		MaxLength:               8,
	}
}

// ComputeAdaptiveLength uses the birthday paradox to pick a hash length
// that keeps collision probability below the configured threshold.
func ComputeAdaptiveLength(numIssues int, cfg AdaptiveIDConfig) int {
	const base = 36.0
	for length := cfg.MinLength; length <= cfg.MaxLength; length++ {
		totalPossibilities := math.Pow(base, float64(length))
		exponent := -float64(numIssues*numIssues) / (2.0 * totalPossibilities)
		prob := 1.0 - math.Exp(exponent)
		if prob <= cfg.MaxCollisionProbability {
			return length
		}
	}
	return cfg.MaxLength
}
