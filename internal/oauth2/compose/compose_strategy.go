// Copyright © 2024 Ory Corp
// SPDX-License-Identifier: Apache-2.0

package compose

import (
	"context"

	fosite "github.com/PRO-Robotech/corelib/internal/oauth2"
	"github.com/PRO-Robotech/corelib/internal/oauth2/handler/oauth2"
	"github.com/PRO-Robotech/corelib/internal/oauth2/handler/openid"
	"github.com/PRO-Robotech/corelib/internal/oauth2/token/hmac"
	"github.com/PRO-Robotech/corelib/internal/oauth2/token/jwt"
)

type CommonStrategy struct {
	oauth2.CoreStrategy
	openid.OpenIDConnectTokenStrategy
	jwt.Signer
}

type HMACSHAStrategyConfigurator interface {
	fosite.AccessTokenLifespanProvider
	fosite.RefreshTokenLifespanProvider
	fosite.AuthorizeCodeLifespanProvider
	fosite.TokenEntropyProvider
	fosite.GlobalSecretProvider
	fosite.RotatedGlobalSecretsProvider
	fosite.HMACHashingProvider
}

func NewOAuth2HMACStrategy(config HMACSHAStrategyConfigurator) *oauth2.HMACSHAStrategy {
	return oauth2.NewHMACSHAStrategy(&hmac.HMACStrategy{Config: config}, config)
}

func NewOAuth2JWTStrategy(keyGetter func(context.Context) (interface{}, error), strategy oauth2.CoreStrategy, config fosite.Configurator) *oauth2.DefaultJWTStrategy {
	return &oauth2.DefaultJWTStrategy{
		Signer:          &jwt.DefaultSigner{GetPrivateKey: keyGetter},
		HMACSHAStrategy: strategy,
		Config:          config,
	}
}

func NewOpenIDConnectStrategy(keyGetter func(context.Context) (interface{}, error), config fosite.Configurator) *openid.DefaultStrategy {
	return &openid.DefaultStrategy{
		Signer: &jwt.DefaultSigner{GetPrivateKey: keyGetter},
		Config: config,
	}
}
