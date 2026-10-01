// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/notify/feed"
)

// CX1-67: сторожа Put — пять различимых значений. errors.Is между двумя
// разными — ложь, сам с собой — истина; обёртка %w с именем шаблона сохраняет
// свой сторож и не становится чужим.
func TestCX167_PutGuardsArePairwiseDistinct(t *testing.T) {
	guards := feed.PutGuards()
	require.Len(t, guards, 5)
	require.ElementsMatch(t, []error{
		feed.ErrDeliveryNotConfigured, feed.ErrAttrsInvalid, feed.ErrRecipientInvalid,
		feed.ErrLimitExhausted, feed.ErrSecondLimitedPut,
	}, guards)
	pairs := 0
	for i, a := range guards {
		for j, b := range guards {
			pairs++
			wrapped := fmt.Errorf("шаблон probe-hello: %w", a)
			if i == j {
				require.True(t, errors.Is(a, b))
				require.True(t, errors.Is(wrapped, b))
				continue
			}
			require.False(t, errors.Is(a, b), "%v ~ %v", a, b)
			require.False(t, errors.Is(wrapped, b), "обёртка %v стала %v", a, b)
		}
	}
	t.Logf("пар сторожей проверено: %d", pairs)
	require.False(t, errors.Is(feed.ErrSourceUnbound, feed.ErrDeliveryNotConfigured))
}
