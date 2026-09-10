// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package authz_test

// cache_stats_test.go — величины кеша положительных вердиктов НАБЛЮДАЕМЫ.
//
// # Предмет
//
// Доля попаданий — единственное число, которым можно ответить «сколько даёт
// кеш». Пока его нет, всякое утверждение о кеше непроверяемо В ОБЕ СТОРОНЫ:
// кеш, который не попадает ни разу, снаружи неотличим от кеша, который
// поглощает весь поток. Считать хиты мало — без промахов у доли нет
// знаменателя, а без размера и вытеснений нечем объяснить, ПОЧЕМУ доля упала
// (окно истекло, потолок выдавил, запись сняли).
//
// # Почему проба утверждает РОСТ, а не наличие
//
// Проба «величина объявлена» зеленеет на кеше, который не работает: счётчик
// есть, серия есть, значение навсегда ноль. Поэтому каждая величина здесь
// утверждается ДВАЖДЫ — сначала как ноль на пустом кеше (положительный
// контроль: попаданий БЕЗ записи не бывает), затем как рост после действия,
// которое обязано её сдвинуть.

import (
	"context"
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/authz"
)

// TestCacheHitsAndMissesAreCounted — попадание и промах считаются раздельно.
func TestCacheHitsAndMissesAreCounted(t *testing.T) {
	c := authz.NewCache(time.Minute)

	// Положительный контроль: на пустом кеше попаданий НЕТ. Без него счётчик,
	// возвращающий константу, прошёл бы проверку «после запроса стало больше».
	if s := c.Stats(); s.Hits != 0 || s.Misses != 0 || s.Entries != 0 {
		t.Fatalf("пустой кеш обязан быть пуст по всем величинам, получено %+v", s)
	}

	if _, hit := c.Get("usr-1", "viewer", "vpc_network", "net-1"); hit {
		t.Fatalf("попадание по ключу, который не записывали")
	}
	if s := c.Stats(); s.Misses != 1 || s.Hits != 0 {
		t.Fatalf("после промаха ожидались misses=1 hits=0, получено %+v", s)
	}

	c.SetAllowed("usr-1", "viewer", "vpc_network", "net-1")
	if s := c.Stats(); s.Entries != 1 || s.Subjects != 1 {
		t.Fatalf("после записи ожидались entries=1 subjects=1, получено %+v", s)
	}

	if _, hit := c.Get("usr-1", "viewer", "vpc_network", "net-1"); !hit {
		t.Fatalf("промах по только что записанному ключу")
	}
	if s := c.Stats(); s.Hits != 1 || s.Misses != 1 {
		t.Fatalf("после попадания ожидались hits=1 misses=1, получено %+v", s)
	}
}

// TestCacheEvictionsAreCountedByReason — вытеснение объясняет себя причиной.
//
// Три причины не сводятся в одну: истечение окна — штатная работа, вытеснение
// по потолку — сигнал, что кеша не хватает на нагрузку, снятие — единственный
// проактивный путь. Свести их значило бы объявить исчерпание потолка нормой.
func TestCacheEvictionsAreCountedByReason(t *testing.T) {
	t.Run("expired", func(t *testing.T) {
		c := authz.NewCache(time.Minute)
		now := time.Now()
		c.SetNowFunc(func() time.Time { return now })
		c.SetAllowed("usr-1", "viewer", "vpc_network", "net-1")
		if s := c.Stats(); s.EvictedExpired != 0 {
			t.Fatalf("до истечения окна вытеснений быть не может, получено %+v", s)
		}
		now = now.Add(2 * time.Minute)
		if _, hit := c.Get("usr-1", "viewer", "vpc_network", "net-1"); hit {
			t.Fatalf("попадание по записи, чьё окно истекло")
		}
		s := c.Stats()
		if s.EvictedExpired != 1 {
			t.Fatalf("ожидалось evicted_expired=1, получено %+v", s)
		}
		if s.EvictedCapacity != 0 {
			t.Fatalf("истечение окна не есть исчерпание потолка, получено %+v", s)
		}
	})

	t.Run("capacity", func(t *testing.T) {
		const limit = 8
		c := authz.NewCacheWithLimit(time.Minute, limit)
		for i := 0; i < limit+1; i++ {
			c.SetAllowed("usr-1", "viewer", "vpc_network", "net-"+string(rune('a'+i)))
		}
		s := c.Stats()
		if s.EvictedCapacity == 0 {
			t.Fatalf("потолок %d превышен, а вытеснений по потолку нет: %+v", limit, s)
		}
		if s.EvictedExpired != 0 {
			t.Fatalf("ни одна запись не истекла, получено %+v", s)
		}
	})

	t.Run("invalidated", func(t *testing.T) {
		c := authz.NewCache(time.Minute)
		c.SetAllowed("usr-1", "viewer", "vpc_network", "net-1")
		c.SetAllowed("usr-1", "viewer", "vpc_network", "net-2")
		c.InvalidateBySubject("usr-1")
		if s := c.Stats(); s.Invalidated != 2 {
			t.Fatalf("ожидалось invalidated=2, получено %+v", s)
		}
		c.SetAllowed("usr-2", "viewer", "vpc_network", "net-3")
		c.InvalidateAll()
		if s := c.Stats(); s.Invalidated != 3 || s.Entries != 0 {
			t.Fatalf("после полного снятия ожидались invalidated=3 entries=0, получено %+v", s)
		}
	})
}

// TestInterceptorReportsItsCacheWindow — звено отдаёт величины ТОГО кеша,
// который спрашивает, а не своей второй копии учёта.
//
// Второе место учёта одной величины расходится с первым молча, поэтому счётчик
// попаданий у звена обязан быть ТЕМ ЖЕ числом, что у его кеша.
func TestInterceptorReportsItsCacheWindow(t *testing.T) {
	cache := authz.NewCache(time.Minute)
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		ServiceName: "kacho-demo",
		Map:         makeMap(),
		Client: authz.CheckClientFunc(func(context.Context, string, string, string) (bool, error) {
			return true, nil
		}),
		Cache: cache,
	})
	ctx := ctxWithPrincipal(t, "usr-1", "user")
	const method = "/kacho.cloud.vpc.v1.NetworkService/Get"

	// Положительный контроль: до единого вызова попаданий нет.
	if m := intr.Metrics(); m.Cache.Hits != 0 || m.Cache.Misses != 0 {
		t.Fatalf("до первого вызова кеш не спрашивали, получено %+v", m)
	}

	if _, err := runUnary(intr, ctx, method, &fakeReq{id: "enp00000000000000001"}); err != nil {
		t.Fatalf("первый вызов: %v", err)
	}
	if m := intr.Metrics(); m.Cache.Misses != 1 || m.Cache.Hits != 0 {
		t.Fatalf("первый вызов обязан быть промахом, получено %+v", m)
	}

	if _, err := runUnary(intr, ctx, method, &fakeReq{id: "enp00000000000000001"}); err != nil {
		t.Fatalf("второй вызов: %v", err)
	}
	m := intr.Metrics()
	if m.Cache.Hits != 1 || m.Cache.Misses != 1 {
		t.Fatalf("повтор того же вопроса обязан быть попаданием, получено %+v", m)
	}
	if got, want := m.Cache.Hits, cache.Stats().Hits; got != want {
		t.Fatalf("звено и его кеш считают попадания по-разному: %d против %d", got, want)
	}
	if cs := intr.CacheStats(); cs.Entries != 1 {
		t.Fatalf("после одного разрешённого вопроса в кеше ожидалась одна запись, получено %+v", cs)
	}
}
