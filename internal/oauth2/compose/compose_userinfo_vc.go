// Copyright © 2024 Ory Corp
// SPDX-License-Identifier: Apache-2.0
// Изменено PRO-Robotech (modified by PRO-Robotech): перечень изменений — internal/oauth2/PROVENANCE.md.

package compose

import (
	fosite "github.com/PRO-Robotech/corelib/internal/oauth2"
	"github.com/PRO-Robotech/corelib/internal/oauth2/handler/verifiable"
)

// OIDCUserinfoVerifiableCredentialFactory creates a verifiable credentials
// handler.
func OIDCUserinfoVerifiableCredentialFactory(config fosite.Configurator, storage, strategy any) any {
	return &verifiable.Handler{
		NonceManager: storage.(verifiable.NonceManager),
		Config:       config,
	}
}
