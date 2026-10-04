// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed_test

// Пробы полосы X2 NTF-3 (приёмка sub-phase-NTF-3, отпечаток ac1f9fc9…): формы
// адресата строки ленты (Р27, NTF3-150, УК3-11), ключ окна «проект» и счётчик
// подавления хуком после коммита (Р14, NTF3-14, УК3-04).
//
// Контракт, который пробы называют (поверхность corelib, выбор полосы RED):
//   - feed.TemplateDesc.Recipient — форма адресата шаблона, тип feed.RecipientForm,
//     значения feed.RecipientAddress | feed.RecipientSubject | feed.RecipientFanout |
//     feed.RecipientAccountOwner (поле recipient в notification.yaml, Р27);
//   - feed.ScopeProject — область лимита «проект», ключ окна — feed.Values.Project (Р14);
//   - notify_suppressed_total{ns,template,reason,scope} — регистрируется в
//     Config.Metrics источника и растёт хуком journaltx.Tx.AfterCommit (З4, З18).

import (
	"context"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/ids"
	"github.com/PRO-Robotech/corelib/notify/feed"
)

func formDesc(form feed.RecipientForm, limits ...feed.Limit) feed.TemplateDesc {
	d := helloDesc(limits...)
	d.Recipient = form
	return d
}

func userSubject() string { return "user:" + ids.NewID(ids.PrefixUser) }

// putRows — постановка в своей транзакции (fixture.put) и число строк ленты
// после коммита: «строк 0» читается из базы, а не выводится из ошибки.
func (f *fixture) putRows(t *testing.T, d feed.TemplateDesc, to string, v feed.Values) (int, error) {
	t.Helper()
	err := f.put(t, d, to, v)
	return f.rows(t), err
}

// УК3-11 (CX3B-27, З3): Put судит значение формы subject семейством usr через
// ids.IsValid и непустотой. Отрицательные кейсы отличаются от близнеца одним
// фактом каждый: семейство id (vol), пустой id, тип субъекта (service_account),
// отсутствие типа, адрес вместо субъекта (NTF3-150).
func TestUK3_11_PutJudgesSubjectByUserFamily(t *testing.T) {
	twin := userSubject()
	cases := []struct {
		name, to string
	}{
		{"id другого семейства", "user:" + ids.NewID(ids.PrefixVolume)},
		{"пустой id", "user:"},
		{"сервисный аккаунт", "service_account:" + ids.NewID(ids.PrefixServiceAccount)},
		{"id без типа", ids.NewID(ids.PrefixUser)},
		{"адрес вместо субъекта", "a@example.test"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, true)
			n, err := f.putRows(t, formDesc(feed.RecipientSubject), c.to, hello())
			require.ErrorIs(t, err, feed.ErrRecipientInvalid, "значение %q формы subject", c.to)
			require.Equal(t, 0, n, "строк ленты после отказа сторожа")
			require.Equal(t, 0, f.signals(t), "сигнала после отказа сторожа")
		})
	}
	t.Run("близнец user:usr", func(t *testing.T) {
		f := newFixture(t, true)
		n, err := f.putRows(t, formDesc(feed.RecipientSubject), twin, hello())
		require.NoError(t, err, "значение %q формы subject — строка ставится", twin)
		require.Equal(t, 1, n)
		require.Equal(t, 1, f.signals(t))
	})
}

// Р27: форма fanout — адресата нет (шаблон resource-event, Р3). Близнец — пустой
// адресат, строка поставлена; отличие — непустой адресат.
func TestNTF3_150_FanoutCarriesNoRecipient(t *testing.T) {
	t.Run("близнец без адресата", func(t *testing.T) {
		f := newFixture(t, true)
		n, err := f.putRows(t, formDesc(feed.RecipientFanout), "", hello())
		require.NoError(t, err)
		require.Equal(t, 1, n)
	})
	t.Run("адресат у fanout", func(t *testing.T) {
		f := newFixture(t, true)
		n, err := f.putRows(t, formDesc(feed.RecipientFanout), userSubject(), hello())
		require.ErrorIs(t, err, feed.ErrRecipientInvalid)
		require.Equal(t, 0, n)
	})
}

// Р27: форма account_owner — значение account:<id>. Близнец — id семейства acc;
// отличия — пустой id и субъект пользователя на месте аккаунта.
func TestNTF3_150_AccountOwnerIsAnAccountID(t *testing.T) {
	t.Run("близнец account:acc", func(t *testing.T) {
		f := newFixture(t, true)
		n, err := f.putRows(t, formDesc(feed.RecipientAccountOwner), "account:"+ids.NewID("acc"), hello())
		require.NoError(t, err)
		require.Equal(t, 1, n)
	})
	for _, c := range []struct{ name, to string }{
		{"пустой id", "account:"},
		{"субъект пользователя", userSubject()},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, true)
			n, err := f.putRows(t, formDesc(feed.RecipientAccountOwner), c.to, hello())
			require.ErrorIs(t, err, feed.ErrRecipientInvalid)
			require.Equal(t, 0, n)
		})
	}
}

// Р27: форма address (NTF-1 Р6) не меняется — адрес ставится; отличие — субъект
// на месте адреса.
func TestNTF3_150_AddressFormIsUnchanged(t *testing.T) {
	t.Run("близнец адрес", func(t *testing.T) {
		f := newFixture(t, true)
		n, err := f.putRows(t, formDesc(feed.RecipientAddress), "a@example.test", hello())
		require.NoError(t, err)
		require.Equal(t, 1, n)
	})
	t.Run("субъект у address", func(t *testing.T) {
		f := newFixture(t, true)
		n, err := f.putRows(t, formDesc(feed.RecipientAddress), userSubject(), hello())
		require.ErrorIs(t, err, feed.ErrRecipientInvalid)
		require.Equal(t, 0, n)
	})
}

// suppressed — серия notify_suppressed_total{ns,template,reason,scope} реестра
// источника; серии нет — 0 и false.
func suppressed(t *testing.T, f *fixture, template, scope string) (float64, bool) {
	t.Helper()
	mfs, err := f.reg.Gather()
	require.NoError(t, err)
	want := map[string]string{"ns": "probe", "template": template, "reason": "limit", "scope": scope}
	for _, mf := range mfs {
		if mf.GetName() != "notify_suppressed_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			if labelsEqual(m.GetLabel(), want) {
				return m.GetCounter().GetValue(), true
			}
		}
	}
	return 0, false
}

func labelsEqual(got []*dto.LabelPair, want map[string]string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, l := range got {
		if want[l.GetName()] != l.GetValue() {
			return false
		}
	}
	return true
}

// exhaustedPut — постановка сверх лимита в открытой транзакции помощника:
// ErrLimitExhausted; возвращённая функция завершает транзакцию коммитом либо откатом.
func (f *fixture) exhaustedPut(t *testing.T, d feed.TemplateDesc, to string, v feed.Values) func(commit bool) {
	t.Helper()
	tx := f.begin(t)
	f.seed(t, tx)
	require.ErrorIs(t, feed.Put(f.ctx, tx, d, to, v), feed.ErrLimitExhausted)
	return func(commit bool) {
		if commit {
			require.NoError(t, tx.Commit(f.ctx))
			return
		}
		require.NoError(t, tx.Rollback(context.Background()))
	}
}

// УК3-04 (CX3-07, З4, З18): лимит исчерпан → транзакция вызывающего откачена →
// notify_suppressed_total не изменился; близнец — коммит → +1. Отличие
// кейсов — один факт: исход транзакции вызывающего.
func TestUK3_04_SuppressedGrowsOnlyAfterCommit(t *testing.T) {
	f := newFixture(t, true)
	// Форма address (NTF-1) — чтобы красный этой пробы называл только счётчик.
	d := formDesc(feed.RecipientAddress, perHour(1))
	to := "a@example.test"
	require.NoError(t, f.put(t, d, to, hello()))

	finish := f.exhaustedPut(t, d, to, hello())
	finish(false)
	v, _ := suppressed(t, f, d.Name, "recipient")
	require.Equal(t, 0.0, v, "откат после ErrLimitExhausted: подавления не было")

	finish = f.exhaustedPut(t, d, to, hello())
	finish(true)
	v, ok := suppressed(t, f, d.Name, "recipient")
	require.True(t, ok, "серии notify_suppressed_total{ns=probe,template=%s,reason=limit,scope=recipient} нет", d.Name)
	require.Equal(t, 1.0, v, "коммит после ErrLimitExhausted: подавление одно")
	require.Equal(t, 1, f.rows(t), "строка сверх лимита не поставлена")
}

func perHourProject(max int32) feed.Limit {
	return feed.Limit{Scope: feed.ScopeProject, WindowSeconds: 3600, Max: max}
}

func inProject(project string) feed.Values {
	v := hello()
	v.Project = project
	return v
}

// Р14, NTF3-14: окно «проект» — ключ Values.Project. Лимит проекта исчерпан
// постановкой другому адресату того же проекта → строка не ставится, после
// коммита подавление со scope=project; близнец — тот же адресат в другом
// проекте → строка поставлена.
func TestNTF3_14_ProjectWindowKeysByProject(t *testing.T) {
	f := newFixture(t, true)
	d := formDesc(feed.RecipientAddress, perHour(20), perHourProject(1))
	prj := ids.NewID("prj")
	require.NoError(t, f.put(t, d, "a@example.test", inProject(prj)))

	other := "b@example.test"
	f.exhaustedPut(t, d, other, inProject(prj))(true)
	v, ok := suppressed(t, f, d.Name, "project")
	require.True(t, ok, "серии scope=project нет")
	require.Equal(t, 1.0, v)
	require.Equal(t, 1, f.rows(t))

	require.NoError(t, f.put(t, d, other, inProject(ids.NewID("prj"))), "близнец: другой проект — окно своё")
	require.Equal(t, 2, f.rows(t))
}

// Р14: исчерпаны оба окна — метка scope=recipient (одно подавление, не два).
func TestNTF3_14_BothWindowsExhaustedCountsRecipient(t *testing.T) {
	f := newFixture(t, true)
	d := formDesc(feed.RecipientAddress, perHour(1), perHourProject(1))
	to, prj := "a@example.test", ids.NewID("prj")
	require.NoError(t, f.put(t, d, to, inProject(prj)))

	f.exhaustedPut(t, d, to, inProject(prj))(true)
	v, ok := suppressed(t, f, d.Name, "recipient")
	require.True(t, ok, "серии scope=recipient нет")
	require.Equal(t, 1.0, v)
	p, _ := suppressed(t, f, d.Name, "project")
	require.Equal(t, 0.0, p, "исчерпаны оба — счёт идёт в recipient")
}
