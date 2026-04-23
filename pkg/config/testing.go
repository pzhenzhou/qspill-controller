package config

import "github.com/pzhenzhou/qspill-controller/pkg/api"

// NewTestRegistry builds a PolicyRegistry for use in tests. It skips
// validation so callers can construct arbitrarily shaped registries.
func NewTestRegistry(defaults Defaults, policies []*api.SpillPolicy) *PolicyRegistry {
	return newPolicyRegistry(defaults, policies)
}
