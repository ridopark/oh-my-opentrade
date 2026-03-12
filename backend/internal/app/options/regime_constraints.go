package options

import "github.com/oh-my-opentrade/backend/internal/domain"

// Type aliases to domain types — all definitions now live in domain/options_config.go.
type RegimeConstraintKey = domain.RegimeConstraintKey
type RegimeConstraintsMap = domain.RegimeConstraintsMap

func DefaultRegimeConstraints() RegimeConstraintsMap {
	return domain.DefaultRegimeConstraints()
}
