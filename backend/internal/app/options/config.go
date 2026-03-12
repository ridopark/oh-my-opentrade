package options

import "github.com/oh-my-opentrade/backend/internal/domain"

// Type aliases to domain types — all definitions now live in domain/options_config.go.
// Existing consumers of this package continue to compile unchanged.
type OptionsConfig = domain.OptionsConfig
type ContractSelectionConstraints = domain.ContractSelectionConstraints
