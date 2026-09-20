// Copyright © 2024 Ory Corp
// SPDX-License-Identifier: Apache-2.0

package compose

import (
	fosite "github.com/PRO-Robotech/corelib/internal/oauth2"
	"github.com/PRO-Robotech/corelib/internal/oauth2/handler/oauth2"
	"github.com/PRO-Robotech/corelib/internal/oauth2/handler/pkce"
)

// OAuth2PKCEFactory creates a PKCE handler.
func OAuth2PKCEFactory(config fosite.Configurator, storage interface{}, strategy interface{}) interface{} {
	return &pkce.Handler{
		AuthorizeCodeStrategy: strategy.(oauth2.AuthorizeCodeStrategy),
		Storage:               storage.(pkce.PKCERequestStorage),
		Config:                config,
	}
}
