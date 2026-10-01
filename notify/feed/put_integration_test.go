// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/ids"
	"github.com/PRO-Robotech/corelib/journaltx"
	"github.com/PRO-Robotech/corelib/notify/feed"
)

// NTF1-B01: постановка в транзакции — после коммита ровно одна строка pending
// и ровно одна строка журнала подписки по объекту notification_feed:probe.
func TestNTF1B01_CommittedPutLeavesOneRowAndOneSignal(t *testing.T) {
	f := newFixture(t, true)
	tx := f.begin(t)
	var txNow time.Time
	require.NoError(t, tx.QueryRow(f.ctx, `SELECT now()`).Scan(&txNow))
	require.NoError(t, feed.Put(f.ctx, tx, helloDesc(), "user@example.invalid", hello()))
	require.NoError(t, tx.Commit(f.ctx))

	require.Equal(t, 1, f.rows(t))
	var (
		id, tmpl, class, state, rcpt string
		rev                          int
		enq, exp                     time.Time
		notBeforeNull                bool
	)
	require.NoError(t, f.pool.QueryRow(context.Background(), `
		SELECT id, template, schema_rev, class, state, recipient_address, enqueued_at, expires_at, not_before IS NULL
		  FROM probe_notification_outbox`).Scan(&id, &tmpl, &rev, &class, &state, &rcpt, &enq, &exp, &notBeforeNull))
	require.True(t, strings.HasPrefix(id, "ntf-"), id)
	require.True(t, ids.IsValidHyphen(id, ids.PrefixNotificationHyphen), id)
	require.Equal(t, "probe-hello", tmpl)
	require.Equal(t, 1, rev)
	require.Equal(t, "notice", class)
	require.Equal(t, "pending", state)
	require.Equal(t, "user@example.invalid", rcpt)
	require.True(t, enq.Equal(txNow), "enqueued_at — время транзакции: %v против %v", enq, txNow)
	require.True(t, exp.Equal(enq.Add(72*time.Hour)), "expires_at = enqueued_at + ttl")
	require.True(t, notBeforeNull, "УК75: строка без DEFER — not_before IS NULL")

	require.Equal(t, 1, f.signals(t))
}

// NTF1-B02: откат после Put — ни строки, ни сигнала, счётчик лимита прежний.
func TestNTF1B02_RollbackLeavesNoRowNoSignalNoCount(t *testing.T) {
	f := newFixture(t, true)
	tx := f.begin(t)
	require.NoError(t, feed.Put(f.ctx, tx, helloDesc(perHour(5)), "user@example.invalid", hello()))
	require.NoError(t, tx.Rollback(f.ctx))
	require.Equal(t, 0, f.rows(t))
	require.Equal(t, 0, f.signals(t))
	require.Equal(t, 0, f.windows(t))
	require.Equal(t, 0, f.count(t, `SELECT count(*) FROM probe_notification_contrib`))
}

func parallelPuts(t *testing.T, f *fixture, n int, d feed.TemplateDesc, to string) (ok, exhausted int) {
	t.Helper()
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		errs []error
	)
	start := make(chan struct{})
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			tx, err := journaltx.Begin(f.ctx, f.pool, journaltx.NewOptions(true))
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				return
			}
			putErr := feed.Put(f.ctx, tx, d, to, hello())
			// Сторож без ошибки SQL: транзакция пригодна к коммиту, и
			// фикстурный вызывающий откатывает её сам (NTF1-B09).
			var finErr error
			if putErr == nil {
				finErr = tx.Commit(f.ctx)
			} else {
				finErr = tx.Rollback(f.ctx)
			}
			mu.Lock()
			defer mu.Unlock()
			switch {
			case finErr != nil:
				errs = append(errs, finErr)
			case putErr == nil:
				ok++
			case errors.Is(putErr, feed.ErrLimitExhausted):
				exhausted++
			default:
				errs = append(errs, putErr)
			}
		}()
	}
	close(start)
	wg.Wait()
	require.Empty(t, errs)
	return ok, exhausted
}

// NTF1-B08: L параллельных постановок при лимите L проходят все.
func TestNTF1B08_LParallelPutsAtLimitLAllPass(t *testing.T) {
	f := newFixture(t, true)
	const l = 6
	ok, ex := parallelPuts(t, f, l, helloDesc(perHour(l)), "user@example.invalid")
	require.Equal(t, l, ok)
	require.Zero(t, ex)
	require.Equal(t, l, f.rows(t))
	require.Equal(t, l, f.windowSum(t))
}

// NTF1-B09: L+k параллельных постановок — строк ровно L, k сторожей
// ErrLimitExhausted без ошибки SQL, счётчик L.
func TestNTF1B09_BeyondLimitExactlyLRows(t *testing.T) {
	f := newFixture(t, true)
	const l, k = 6, 4
	ok, ex := parallelPuts(t, f, l+k, helloDesc(perHour(l)), "user@example.invalid")
	require.Equal(t, l, ok)
	require.Equal(t, k, ex)
	require.Equal(t, l, f.rows(t))
	require.Equal(t, l, f.windowSum(t))
	require.Equal(t, l, f.count(t, `SELECT count(*) FROM probe_notification_contrib`))
}

// NTF1-B27: окно лимита — одно на ящик, как бы адрес ни написан; близнец —
// два ящика; адрес, который Normalize не разбирает, — ErrRecipientInvalid.
func TestNTF1B27_OneWindowPerMailbox(t *testing.T) {
	const l = 3
	d := helloDesc(perHour(l))

	t.Run("регистр домена — одно окно", func(t *testing.T) {
		f := newFixture(t, true)
		for range l {
			require.NoError(t, f.put(t, d, "User@Example.Invalid", hello()))
		}
		require.ErrorIs(t, f.put(t, d, "User@example.invalid", hello()), feed.ErrLimitExhausted)
		require.Equal(t, l, f.rows(t))
		require.Equal(t, 1, f.windows(t))
		require.Equal(t, l, f.windowSum(t))
	})
	t.Run("близнец: локальная часть различается — два окна", func(t *testing.T) {
		f := newFixture(t, true)
		for range l {
			require.NoError(t, f.put(t, d, "user@example.invalid", hello()))
		}
		require.NoError(t, f.put(t, d, "User@example.invalid", hello()))
		require.Equal(t, l+1, f.rows(t))
		require.Equal(t, 2, f.windows(t))
	})
	t.Run("IDNA: Unicode и A-label — одно окно", func(t *testing.T) {
		f := newFixture(t, true)
		for range l {
			require.NoError(t, f.put(t, d, "user@bücher.example.invalid", hello()))
		}
		require.ErrorIs(t, f.put(t, d, "user@xn--bcher-kva.example.invalid", hello()), feed.ErrLimitExhausted)
		require.Equal(t, l, f.rows(t))
		require.Equal(t, 1, f.windows(t))
	})
	t.Run("IDNA: ß и ss — два окна", func(t *testing.T) {
		f := newFixture(t, true)
		for range l {
			require.NoError(t, f.put(t, d, "user@straße.example.invalid", hello()))
		}
		require.NoError(t, f.put(t, d, "user@strasse.example.invalid", hello()))
		require.Equal(t, l+1, f.rows(t))
		require.Equal(t, 2, f.windows(t))
	})
	t.Run("адрес не разбирается — сторож, транзакция коммитится", func(t *testing.T) {
		f := newFixture(t, true)
		require.NoError(t, f.put(t, d, "user@example.invalid", hello()))
		for _, bad := range []string{"user@", "us\r\ner@example.invalid", "", "user"} {
			err := f.put(t, d, bad, hello())
			require.ErrorIs(t, err, feed.ErrRecipientInvalid, "%q", bad)
			if bad != "" {
				require.NotContains(t, err.Error(), bad)
			}
		}
		require.Equal(t, 1, f.rows(t))
		require.Equal(t, 1, f.windowSum(t), "счётчики не изменились")
		require.Equal(t, 1, f.signals(t))
	})
}

// G24 (а)–(ж), (и)–(л): значения вне формы при остальных по форме.
var g24 = []struct {
	attr, value string
}{
	{"target", "//other.example.invalid/x"},
	{"target", "https://other.example.invalid/x"},
	{"target", `/\other.example.invalid`},
	{"target", "/a/../b"},
	{"target", "/a b"},
	{"token", "abcdefghijklmn&next=x"},
	{"token", "abcdefghijklmno"},
	{"target", "/a/"},
	{"target", "/a//b"},
	{"subject_name", "a\r\nBcc: x@example.invalid"},
}

// NTF1-B28 (Put), УК40, УК42: значение вне формы и описание мимо генератора —
// сторож ErrAttrsInvalid с именем атрибута, строк 0, транзакция коммитится.
func TestNTF1B28_ValueOutOfFormIsAGuardNotASilentLoss(t *testing.T) {
	f := newFixture(t, true)
	for _, c := range g24 {
		v := linkTwin()
		v[c.attr] = c.value
		err := f.put(t, linkDesc(), "user@example.invalid", feed.Values{Attrs: v})
		requireNamed(t, err, feed.ErrAttrsInvalid, c.attr, c.value)
	}
	// (а) атрибут вне описания
	v := linkTwin()
	v["extra"] = "x"
	requireNamed(t, f.put(t, linkDesc(), "user@example.invalid", feed.Values{Attrs: v}), feed.ErrAttrsInvalid, "extra")
	// (б) пустое описание атрибутов при непустом наборе значений
	empty := linkDesc()
	empty.Attrs = nil
	requireNamed(t, f.put(t, empty, "user@example.invalid", feed.Values{Attrs: linkTwin()}), feed.ErrAttrsInvalid, "subject_name")
	require.Equal(t, 0, f.rows(t))
	require.Equal(t, 0, f.signals(t))
	require.Equal(t, 0, f.windows(t))

	// близнец: значения близнеца G24 — по строке на вызов
	require.NoError(t, f.put(t, linkDesc(), "user@example.invalid", feed.Values{Attrs: linkTwin()}))
	require.Equal(t, 1, f.rows(t))
}

// NTF1-B29 (Put, ревизия): нуль объявленного атрибута не ставится; описание
// с ревизией 0 — сторож с именем schema_rev; база ревизию 0 не принимает.
func TestNTF1B29_ZeroOfDeclaredAttributeIsAGuard(t *testing.T) {
	f := newFixture(t, true)
	zeros := map[string]any{
		"subject_name": "", "note": "", "code": "", "target": "", "token": "", "issued_at": time.Time{},
	}
	for name, zero := range zeros {
		v := allTwin()
		v[name] = zero
		requireNamed(t, f.put(t, allDesc(), "user@example.invalid", feed.Values{Attrs: v}), feed.ErrAttrsInvalid, name)
	}
	// (Put) без атрибута note
	v := allTwin()
	delete(v, "note")
	requireNamed(t, f.put(t, allDesc(), "user@example.invalid", feed.Values{Attrs: v}), feed.ErrAttrsInvalid, "note")
	// (ревизия)
	rev0 := allDesc()
	rev0.SchemaRev = 0
	requireNamed(t, f.put(t, rev0, "user@example.invalid", feed.Values{Attrs: allTwin()}), feed.ErrAttrsInvalid, "schema_rev")
	require.Equal(t, 0, f.rows(t))
	require.Equal(t, 0, f.signals(t))

	// близнец: значения по форме — одна строка, schema_rev = 1, секрет запечатан
	require.NoError(t, f.put(t, allDesc(), "user@example.invalid", feed.Values{Attrs: allTwin()}))
	require.Equal(t, 1, f.rows(t))
	var (
		rev      int
		attrsTxt string
		sealed   []byte
	)
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT schema_rev, attrs::text, secret_attrs FROM probe_notification_outbox`).Scan(&rev, &attrsTxt, &sealed))
	require.Equal(t, 1, rev)
	// значение 123456 — подстрока токена близнеца, поэтому судится ключ
	require.NotContains(t, attrsTxt, `"code"`, "секрет в attrs открытым текстом")
	require.Contains(t, attrsTxt, `"issued_at": "2026-09-30T00:00:00Z"`)
	require.NotEmpty(t, sealed)

	_, err := f.pool.Exec(context.Background(), `UPDATE probe_notification_outbox SET schema_rev = 0`)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "23514", pgErr.Code)
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM probe_notification_outbox WHERE schema_rev = 1`))
}

// NTF1-B30 (Put), УК58: optional, переданный нулём, — сторож; «не задан»
// выражается только отсутствием ключа; нуль required — сторож.
func TestNTF1B30_OptionalZeroIsAbsenceOfKey(t *testing.T) {
	f := newFixture(t, true)
	requireNamed(t, f.put(t, optDesc(), "user@example.invalid",
		attrs("subject_name", "probe", "inviter", "")), feed.ErrAttrsInvalid, "inviter")
	requireNamed(t, f.put(t, optDesc(), "user@example.invalid",
		attrs("subject_name", "probe", "inviter", "i", "target", "//a")), feed.ErrAttrsInvalid, "target")
	requireNamed(t, f.put(t, optDesc(), "user@example.invalid",
		attrs("subject_name", "")), feed.ErrAttrsInvalid, "subject_name")
	require.Equal(t, 0, f.rows(t))

	// (а) без ключей optional — строка без ключей
	require.NoError(t, f.put(t, optDesc(), "user@example.invalid", attrs("subject_name", "probe")))
	var a string
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT attrs::text FROM probe_notification_outbox`).Scan(&a))
	require.NotContains(t, a, "inviter")
	require.NotContains(t, a, "target")
	// (б) оба заданы — строка несёт оба
	require.NoError(t, f.put(t, optDesc(), "user@example.invalid",
		attrs("subject_name", "probe", "inviter", "i", "target", "/a")))
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM probe_notification_outbox
		WHERE attrs->>'inviter' = 'i' AND attrs->>'target' = '/a'`))
}

// NTF1-N05: флаг выключен — notice возвращает nil, строк и событий 0,
// мутация глагола закоммичена. Близнец — включено (B01).
func TestNTF1N05_FlagOffNoticeWritesNothing(t *testing.T) {
	f := newFixture(t, false)
	require.NoError(t, f.put(t, helloDesc(perHour(1)), "user@example.invalid", hello()))
	require.Equal(t, 0, f.rows(t))
	require.Equal(t, 0, f.signals(t))
	require.Equal(t, 0, f.windows(t))
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM probe_items`))

	on := fixtureOn(t, f.pool, true)
	require.NoError(t, on.put(t, helloDesc(perHour(1)), "user@example.invalid", hello()))
	require.Equal(t, 1, on.rows(t))
	require.Equal(t, 1, on.signals(t))
}

// NTF1-N06: флаг выключен — письмо security отвергнуто одним сторожем для
// любого адреса; глагол спрашивает флаг первым (DeliveryConfigured).
func TestNTF1N06_FlagOffSecurityIsAnExplicitRefusal(t *testing.T) {
	sec := helloDesc(perHour(3))
	sec.Class = feed.ClassSecurity
	f := newFixture(t, false)
	require.ErrorIs(t, f.src.DeliveryConfigured(), feed.ErrDeliveryNotConfigured)
	for _, to := range []string{"a@example.invalid", "b@example.invalid", "user@"} {
		err := f.put(t, sec, to, hello())
		require.ErrorIs(t, err, feed.ErrDeliveryNotConfigured)
		require.Equal(t, feed.ErrDeliveryNotConfigured.Error(), err.Error(), "отказ одинаков для любого адреса")
	}
	require.Equal(t, 0, f.rows(t))

	on := fixtureOn(t, f.pool, true)
	require.NoError(t, on.src.DeliveryConfigured())
	require.NoError(t, on.put(t, sec, "a@example.invalid", hello()))
	require.Equal(t, 1, on.rows(t))
}

// NTF1-N07 (схема та же): схема ленты от флага не зависит — источник с
// любым флагом поднимается над одной и той же схемой и её не меняет.
func TestNTF1N07_SchemaIsTheSameUnderEitherFlag(t *testing.T) {
	f := newFixture(t, false)
	catalog := func() string {
		var s string
		require.NoError(t, f.pool.QueryRow(context.Background(), `
			SELECT string_agg(table_name || '.' || column_name || ':' || data_type, ',' ORDER BY table_name, column_name)
			  FROM information_schema.columns WHERE table_name LIKE 'probe_notification_%'`).Scan(&s))
		return s
	}
	off := catalog()
	_ = fixtureOn(t, f.pool, true)
	require.Equal(t, off, catalog())
	require.NotEmpty(t, off)
}

// NTF1-N09: метрика состояния флага.
func TestNTF1N09_FlagMetric(t *testing.T) {
	on := newFixture(t, true)
	require.Equal(t, 1.0, metricValue(t, on.src.EnabledGauge()))
	off := fixtureOn(t, on.pool, false)
	require.Equal(t, 0.0, metricValue(t, off.src.EnabledGauge()))
	mfs, err := on.reg.Gather()
	require.NoError(t, err)
	series := 0
	for _, mf := range mfs {
		if mf.GetName() == "kacho_notifications_enabled" {
			series += len(mf.GetMetric())
			require.Equal(t, "module", mf.GetMetric()[0].GetLabel()[0].GetName())
			require.Equal(t, "probe", mf.GetMetric()[0].GetLabel()[0].GetValue())
		}
	}
	require.Equal(t, 1, series)
}

// УК77: лимиты «1 в час» и «3 в сутки» — две строки окна одной области;
// четыре постановки в разные часы → строк 3, четвёртая ErrLimitExhausted.
// Ход часов имитируется сдвигом часовых окон в прошлое: суточное окно то же.
func TestUK77_TwoWindowsOfOneScopeAreTwoLimits(t *testing.T) {
	d := helloDesc(perHour(1), feed.Limit{Scope: feed.ScopeRecipient, WindowSeconds: 86400, Max: 3})
	nextHour := func(f *fixture) {
		_, err := f.pool.Exec(context.Background(),
			`UPDATE probe_notification_window SET window_start = window_start - interval '1 hour' WHERE window_seconds = 3600`)
		require.NoError(t, err)
	}
	f := newFixture(t, true)
	for i := range 3 {
		require.NoError(t, f.put(t, d, "user@example.invalid", hello()), "постановка %d", i+1)
		require.ErrorIs(t, f.put(t, d, "user@example.invalid", hello()), feed.ErrLimitExhausted, "тот же час")
		nextHour(f)
	}
	require.ErrorIs(t, f.put(t, d, "user@example.invalid", hello()), feed.ErrLimitExhausted, "сутки исчерпаны")
	require.Equal(t, 3, f.rows(t))
	require.Equal(t, 3, f.count(t, `SELECT count FROM probe_notification_window WHERE window_seconds = 86400`))
	require.Equal(t, 6, f.count(t, `SELECT count(*) FROM probe_notification_contrib`), "по вкладу на лимит")

	// близнец — один лимит «1 в час»: четыре часа — четыре строки
	g := newFixture(t, true)
	for range 4 {
		require.NoError(t, g.put(t, helloDesc(perHour(1)), "user@example.invalid", hello()))
		nextHour(g)
	}
	require.Equal(t, 4, g.rows(t))

	// описание с повтором пары (scope, window) не доходит до SQL
	dup := helloDesc(perHour(1), perHour(2))
	require.ErrorIs(t, g.put(t, dup, "user@example.invalid", hello()), feed.ErrAttrsInvalid)
}

// УК78: окно инициатора исчерпано — ErrLimitExhausted, вызывающий коммитит,
// окно адресата не изменилось; близнец — оба окна свободны, оба +1.
func TestUK78_ExhaustedInitiatorLeavesRecipientWindowUntouched(t *testing.T) {
	d := helloDesc(feed.Limit{Scope: feed.ScopeInitiator, WindowSeconds: 3600, Max: 1}, perHour(5))
	f := newFixture(t, true)
	v := hello()
	v.Initiator = "user:usr-a"
	require.NoError(t, f.put(t, d, "r1@example.invalid", v))
	require.Equal(t, 2, f.windows(t))
	require.Equal(t, 2, f.windowSum(t), "близнец: оба окна +1")

	require.ErrorIs(t, f.put(t, d, "r2@example.invalid", v), feed.ErrLimitExhausted)
	require.Equal(t, 2, f.windows(t), "окно адресата r2 не заведено")
	require.Equal(t, 2, f.windowSum(t))
	require.Equal(t, 1, f.rows(t))
	require.Equal(t, 2, f.count(t, `SELECT count(*) FROM probe_notification_contrib`))

	// инициатора нет при лимите на инициатора — сторож с именем
	requireNamed(t, f.put(t, d, "r3@example.invalid", hello()), feed.ErrAttrsInvalid, "initiator")
}

// УК75: «отсрочки не было» — NULL; бесконечность в колонке невыразима.
func TestUK75_NotBeforeIsNullOrFinite(t *testing.T) {
	f := newFixture(t, true)
	require.NoError(t, f.put(t, helloDesc(), "user@example.invalid", hello()))
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM probe_notification_outbox WHERE not_before IS NULL`))
	_, err := f.pool.Exec(context.Background(), `UPDATE probe_notification_outbox SET not_before = '-infinity'`)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "23514", pgErr.Code)
	_, err = f.pool.Exec(context.Background(), `UPDATE probe_notification_outbox SET not_before = now() + interval '1 minute'`)
	require.NoError(t, err, "близнец — конечный момент")
}

// УК85, CX1-67: две постановки с лимитами в одной транзакции — вторая
// ErrSecondLimitedPut, окна и лента ей не изменены, счётчик дефекта +1.
func TestUK85_SecondLimitedPutInOneTransactionIsRefused(t *testing.T) {
	d := helloDesc(perHour(5))
	defects := func(f *fixture) float64 {
		return metricValue(t, f.src.DefectCounter().WithLabelValues("probe", "second_limited_put"))
	}

	t.Run("вторая с лимитами — сторож", func(t *testing.T) {
		f := newFixture(t, true)
		tx := f.begin(t)
		require.NoError(t, feed.Put(f.ctx, tx, d, "a@example.invalid", hello()))
		require.ErrorIs(t, feed.Put(f.ctx, tx, d, "b@example.invalid", hello()), feed.ErrSecondLimitedPut)
		require.NoError(t, tx.Commit(f.ctx))
		require.Equal(t, 1, f.rows(t))
		require.Equal(t, 1, f.windows(t))
		require.Equal(t, 1.0, defects(f))
	})
	t.Run("близнец: первая ErrLimitExhausted, вторая проходит", func(t *testing.T) {
		f := newFixture(t, true)
		one := helloDesc(perHour(1))
		require.NoError(t, f.put(t, one, "a@example.invalid", hello()))
		tx := f.begin(t)
		require.ErrorIs(t, feed.Put(f.ctx, tx, one, "a@example.invalid", hello()), feed.ErrLimitExhausted)
		require.NoError(t, feed.Put(f.ctx, tx, one, "b@example.invalid", hello()))
		require.NoError(t, tx.Commit(f.ctx))
		require.Equal(t, 2, f.rows(t))
		require.Equal(t, 0.0, defects(f), "ErrLimitExhausted счётчик дефекта не трогает")
	})
	t.Run("близнец: одна с лимитами и две без — проходят", func(t *testing.T) {
		f := newFixture(t, true)
		tx := f.begin(t)
		require.NoError(t, feed.Put(f.ctx, tx, helloDesc(), "a@example.invalid", hello()))
		require.NoError(t, feed.Put(f.ctx, tx, d, "a@example.invalid", hello()))
		require.NoError(t, feed.Put(f.ctx, tx, helloDesc(), "b@example.invalid", hello()))
		require.NoError(t, tx.Commit(f.ctx))
		require.Equal(t, 3, f.rows(t))
	})
	t.Run("инъекция: отметка снята — вторая проходит", func(t *testing.T) {
		// Сторож держится ровно отметкой транзакции: сняв её, вторая
		// постановка проходит — проба выше на этом входе была бы красной.
		f := newFixture(t, true)
		tx := f.begin(t)
		require.NoError(t, feed.Put(f.ctx, tx, d, "a@example.invalid", hello()))
		_, err := tx.Exec(f.ctx, `SELECT set_config('kacho_feed.limited_put', '', true)`)
		require.NoError(t, err)
		require.NoError(t, feed.Put(f.ctx, tx, d, "b@example.invalid", hello()))
		require.NoError(t, tx.Rollback(f.ctx))
	})
}

// УК86 (б): убывающее окно одной области — сторож с именем шаблона до SQL;
// близнец — возрастающее проходит.
func TestUK86_DecreasingLimitsAreRefusedByPut(t *testing.T) {
	f := newFixture(t, true)
	day := feed.Limit{Scope: feed.ScopeRecipient, WindowSeconds: 86400, Max: 3}
	err := f.put(t, helloDesc(day, perHour(1)), "user@example.invalid", hello())
	requireNamed(t, err, feed.ErrAttrsInvalid, "probe-hello")
	require.Equal(t, 0, f.windows(t))
	require.NoError(t, f.put(t, helloDesc(perHour(1), day), "user@example.invalid", hello()))
	require.Equal(t, 2, f.windows(t))
}

// Put без привязанного источника — отказ проводки, не сторож и не тихий nil.
func TestPutWithoutABoundSourceIsAWiringError(t *testing.T) {
	err := feed.Put(context.Background(), nil, helloDesc(), "user@example.invalid", hello())
	require.ErrorIs(t, err, feed.ErrSourceUnbound)
	for _, g := range feed.PutGuards() {
		require.False(t, errors.Is(err, g))
	}
}
