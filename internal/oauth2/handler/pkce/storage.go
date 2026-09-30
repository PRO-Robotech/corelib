// Copyright © 2024 Ory Corp
// SPDX-License-Identifier: Apache-2.0
// Изменено PRO-Robotech (modified by PRO-Robotech): перечень изменений — internal/oauth2/PROVENANCE.md.

package pkce

import (
	"context"

	fosite "github.com/PRO-Robotech/corelib/internal/oauth2"
)

type PKCERequestStorage interface {
	GetPKCERequestSession(ctx context.Context, signature string, session fosite.Session) (fosite.Requester, error)
	CreatePKCERequestSession(ctx context.Context, signature string, requester fosite.Requester) error
	DeletePKCERequestSession(ctx context.Context, signature string) error
}
