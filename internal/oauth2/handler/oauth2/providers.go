// Copyright © 2024 Ory Corp
// SPDX-License-Identifier: Apache-2.0

package oauth2

import fosite "github.com/PRO-Robotech/corelib/internal/oauth2"

type LifespanConfigProvider interface {
	fosite.AccessTokenLifespanProvider
	fosite.RefreshTokenLifespanProvider
	fosite.AuthorizeCodeLifespanProvider
}
