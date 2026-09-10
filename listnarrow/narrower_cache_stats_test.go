// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package listnarrow

// narrower_cache_stats_test.go — окно вердиктов сужателя ОТЧИТЫВАЕТСЯ.
//
// Предмет (#768). У сужателя был размер окна (`CacheSize`) и не было ни одного
// счётчика попаданий. Между тем через него идёт БОЛЬШЕ вопросов, чем через окно
// звена решения: звено задаёт один вопрос на вызов, а сужатель — по вопросу на
// КАЖДЫЙ элемент страницы, а страница контрактно бывает до тысячи. То есть
// величина, которая решает, сколько вопросов доезжает до владельца модели под
// списочной нагрузкой, была непроверяема в обе стороны.
//
// Пробы утверждают РОСТ, а не наличие: счётчик существует и стоит нулём при
// любом состоянии, поэтому его наличие не отличает работающее окно от мёртвого.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// recordingPeer — подставная приёмная сторона, считающая ЗАДАННЫЕ ей вопросы.
//
// Живёт здесь, а не берётся из `narrowtest`: тот пакет импортирует сужатель, и
// проба внутри самого пакета замкнула бы цикл.
type recordingPeer struct {
	allow map[string]bool
	calls int
}

func (p *recordingPeer) BatchCheck(_ context.Context, checks []Check) ([]bool, error) {
	p.calls++
	out := make([]bool, 0, len(checks))
	for _, c := range checks {
		out = append(out, p.allow[c.ResourceID])
	}
	return out, nil
}

// TestVerdictWindowCountsHitsAndMisses — ядро #768.
func TestVerdictWindowCountsHitsAndMisses(t *testing.T) {
	peer := &recordingPeer{allow: map[string]bool{"net1": true, "net2": true}}
	n := New(peer, Config{Relations: map[string][]string{"network": {"v_get"}}, CacheTTL: time.Minute})

	require.Equal(t, uint64(0), n.CacheStats().Hits)
	require.Equal(t, uint64(0), n.CacheStats().Misses)

	// Первый проход: окно пусто ⇒ два ПРОМАХА, оба вопроса уезжают соседу.
	got, err := n.Visible(context.Background(), "usr_a", "network", "act", "v_get", []string{"net1", "net2"})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"net1", "net2"}, got)

	first := n.CacheStats()
	require.Equal(t, uint64(2), first.Misses, "холодное окно обязано считать промахи")
	require.Equal(t, uint64(0), first.Hits)
	require.Equal(t, 2, first.Entries, "размер окна — та величина, которой доля попаданий объясняется")

	// Второй проход по тем же объектам: два ПОПАДАНИЯ, соседа не тревожим.
	callsBefore := peer.calls
	_, err = n.Visible(context.Background(), "usr_a", "network", "act", "v_get", []string{"net1", "net2"})
	require.NoError(t, err)

	second := n.CacheStats()
	require.Equal(t, uint64(2), second.Hits, "повторный вопрос обязан вырастить попадания")
	require.Equal(t, first.Misses, second.Misses, "промахи не растут на попадании")
	require.Equal(t, callsBefore, peer.calls,
		"ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: попадание обязано означать НЕ заданный соседу вопрос, "+
			"иначе счётчик считал бы что-то другое")
}

// TestVerdictWindowCountsExpiryAndCapacityApart — окно съедается по двум разным
// причинам, и складывать их нельзя: истечение есть штатная работа, а давление
// потолка — сигнал, что окна не хватает на нагрузку. Сложенные, они объявили бы
// исчерпание потолка нормой.
func TestVerdictWindowCountsExpiryAndCapacityApart(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	peer := &recordingPeer{allow: map[string]bool{"a": true, "b": true, "c": true}}

	t.Run("истечение окна", func(t *testing.T) {
		n := New(peer, Config{Relations: map[string][]string{"t": {"v_get"}}, CacheTTL: time.Minute}).
			WithClock(func() time.Time { return now })
		_, err := n.Visible(context.Background(), "u", "t", "act", "v_get", []string{"a"})
		require.NoError(t, err)

		n.WithClock(func() time.Time { return now.Add(2 * time.Minute) })
		_, err = n.Visible(context.Background(), "u", "t", "act", "v_get", []string{"a"})
		require.NoError(t, err)

		s := n.CacheStats()
		require.Equal(t, uint64(1), s.EvictedExpired, "запись обязана быть снята ПО ИСТЕЧЕНИИ")
		require.Zero(t, s.EvictedCapacity, "потолок здесь ни при чём — не смешивать")
	})

	t.Run("давление потолка", func(t *testing.T) {
		n := New(peer, Config{Relations: map[string][]string{"t": {"v_get"}},
			CacheTTL: time.Hour, CacheMaxEntries: 2}).
			WithClock(func() time.Time { return now })
		_, err := n.Visible(context.Background(), "u", "t", "act", "v_get", []string{"a", "b", "c"})
		require.NoError(t, err)

		s := n.CacheStats()
		require.Equal(t, uint64(1), s.EvictedCapacity,
			"ещё живая запись, снятая потолком, — это попадание, которого не будет")
		require.Zero(t, s.EvictedExpired, "истечение здесь ни при чём — не смешивать")
		require.Equal(t, 2, s.Entries)
	})
}
