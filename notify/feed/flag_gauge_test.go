// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/notify/feed"
)

// enabledSeries — серии kacho_notifications_enabled реестра: модуль → значение.
func enabledSeries(t *testing.T, reg *prometheus.Registry) map[string]float64 {
	t.Helper()
	mfs, err := reg.Gather()
	require.NoError(t, err)
	out := map[string]float64{}
	for _, mf := range mfs {
		if mf.GetName() != "kacho_notifications_enabled" {
			continue
		}
		for _, m := range mf.GetMetric() {
			require.Len(t, m.GetLabel(), 1)
			require.Equal(t, "module", m.GetLabel()[0].GetName())
			out[m.GetLabel()[0].GetValue()] = m.GetGauge().GetValue()
		}
	}
	return out
}

func parsed(t *testing.T, raw string) feed.Enabled {
	t.Helper()
	en, err := feed.ParseEnabled(flagVar, lookup(map[string]string{flagVar: raw}))
	require.NoError(t, err)
	return en
}

// NTF1-N09 (kacho#2918 S1-A4): серия флага появляется и при флаге 0 — «выключено»
// отличимо от «серии нет»; при флаге 1 — значение 1.
func TestNTF1N09_RegisterEnabledGaugeSeriesExistsAtZero(t *testing.T) {
	reg := prometheus.NewRegistry()
	g, err := feed.RegisterEnabledGauge(reg, "vpc", parsed(t, "false"))
	require.NoError(t, err)
	require.NotNil(t, g)
	require.Equal(t, map[string]float64{"vpc": 0}, enabledSeries(t, reg))

	_, err = feed.RegisterEnabledGauge(reg, "compute", parsed(t, "true"))
	require.NoError(t, err, "второй модуль того же реестра берёт уже зарегистрированное семейство")
	require.Equal(t, map[string]float64{"vpc": 0, "compute": 1}, enabledSeries(t, reg))
}

// Отказы: неразобранный флаг, имя модуля не DNS-метка, реестра нет. Серия при
// отказе не заводится.
func TestNTF1N09_RegisterEnabledGaugeRefusesUnjudgedInput(t *testing.T) {
	reg := prometheus.NewRegistry()
	_, err := feed.RegisterEnabledGauge(reg, "vpc", feed.Enabled{})
	require.ErrorContains(t, err, "vpc")
	_, err = feed.RegisterEnabledGauge(reg, "Not_DNS", parsed(t, "true"))
	require.ErrorContains(t, err, "Not_DNS")
	_, err = feed.RegisterEnabledGauge(nil, "vpc", parsed(t, "true"))
	require.Error(t, err)
	require.Empty(t, enabledSeries(t, reg))
}

type nopSignal struct{}

func (nopSignal) SignalFeed(context.Context, pgx.Tx) error { return nil }

// NewSource ставит серию тем же путём: у выключенного источника серия есть со
// значением 0, и EnabledGauge — та же серия, что видит реестр.
func TestNTF1N09_NewSourceRegistersTheGaugeAtZero(t *testing.T) {
	reg := prometheus.NewRegistry()
	src, err := feed.NewSource(feed.Config{
		Module: "vpc", Service: "vpc", Enabled: parsed(t, "false"),
		Signal: nopSignal{}, Metrics: reg,
	})
	require.NoError(t, err)
	require.Equal(t, map[string]float64{"vpc": 0}, enabledSeries(t, reg))
	src.EnabledGauge().Set(1)
	require.Equal(t, map[string]float64{"vpc": 1}, enabledSeries(t, reg), "EnabledGauge — серия реестра")
}
