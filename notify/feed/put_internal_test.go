// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/notify/address"
)

// NTF1-B27 (нуль): нулевой address.Normalized у ключа окна — ErrRecipientInvalid
// с причиной address.ErrUnset; ключа «пустого адреса» нет. Близнец — значение
// из Normalize.
func TestNTF1B27_ZeroNormalizedAtTheWindowKeyIsRefused(t *testing.T) {
	_, err := recipientKey(address.Normalized{})
	require.ErrorIs(t, err, ErrRecipientInvalid)
	require.ErrorIs(t, err, address.ErrUnset)
	require.False(t, errors.Is(err, ErrAttrsInvalid))

	n, err := address.Normalize("user@example.invalid")
	require.NoError(t, err)
	k, err := recipientKey(n)
	require.NoError(t, err)
	require.Equal(t, "user@example.invalid", k)
}
