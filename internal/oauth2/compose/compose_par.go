// Copyright © 2024 Ory Corp
// SPDX-License-Identifier: Apache-2.0

package compose

import (
	fosite "github.com/PRO-Robotech/corelib/internal/oauth2"
	"github.com/PRO-Robotech/corelib/internal/oauth2/handler/par"
)

// PushedAuthorizeHandlerFactory creates the basic PAR handler
func PushedAuthorizeHandlerFactory(config fosite.Configurator, storage interface{}, strategy interface{}) interface{} {
	return &par.PushedAuthorizeHandler{
		Storage: storage,
		Config:  config,
	}
}
