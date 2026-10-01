// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"

	"github.com/PRO-Robotech/corelib/notify/feed"
)

func lookup(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := env[k]; return v, ok }
}

const flagVar = "KACHO_PROBE_NOTIFICATIONS_ENABLED"

// УК20, УК26, NTF1-N08: флаг принимает ровно true или false. Незаданная
// переменная, пустая строка и иные написания (1, t, TRUE) — отказ с именем
// переменной.
func TestUK20_FlagAcceptsExactlyTrueOrFalse(t *testing.T) {
	on, err := feed.ParseEnabled(flagVar, lookup(map[string]string{flagVar: "true"}))
	require.NoError(t, err)
	require.True(t, on.On())
	off, err := feed.ParseEnabled(flagVar, lookup(map[string]string{flagVar: "false"}))
	require.NoError(t, err)
	require.False(t, off.On())

	_, err = feed.ParseEnabled(flagVar, lookup(nil))
	require.Error(t, err)
	require.Contains(t, err.Error(), flagVar)
	for _, bad := range []string{"", "1", "0", "t", "f", "TRUE", "False", " true", "true ", "yes"} {
		_, err := feed.ParseEnabled(flagVar, lookup(map[string]string{flagVar: bad}))
		require.Error(t, err, "%q", bad)
		require.Contains(t, err.Error(), flagVar, "%q", bad)
	}
	require.False(t, feed.Enabled{}.Set(), "нулевое значение — «не разобрано»")
	require.True(t, on.Set())
}

// NTF1-N06: единый статус отказа «доставка не настроена».
func TestNTF1N06_DeliveryNotConfiguredStatusIsOne(t *testing.T) {
	st := feed.DeliveryNotConfiguredStatus()
	require.Equal(t, codes.FailedPrecondition, st.Code())
	require.Equal(t, "email delivery is not configured in this installation", st.Message())
	var info *errdetails.ErrorInfo
	for _, d := range st.Details() {
		if i, ok := d.(*errdetails.ErrorInfo); ok {
			info = i
		}
	}
	require.NotNil(t, info)
	require.Equal(t, "NOTIFICATION_DELIVERY_NOT_CONFIGURED", info.GetReason())
	b1, err := feed.DeliveryNotConfiguredStatus().Proto().GetDetails()[0].Value, error(nil)
	require.NoError(t, err)
	require.Equal(t, b1, st.Proto().GetDetails()[0].Value, "ответы побайтово равны")
}
