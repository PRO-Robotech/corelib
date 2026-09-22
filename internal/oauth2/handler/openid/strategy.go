// Copyright © 2024 Ory Corp
// SPDX-License-Identifier: Apache-2.0
// Изменено PRO-Robotech (modified by PRO-Robotech): перечень изменений — internal/oauth2/PROVENANCE.md.

package openid

import (
	"context"
	"time"

	fosite "github.com/PRO-Robotech/corelib/internal/oauth2"
)

type OpenIDConnectTokenStrategy interface {
	GenerateIDToken(ctx context.Context, lifespan time.Duration, requester fosite.Requester) (token string, err error)
}
