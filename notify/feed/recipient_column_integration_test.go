// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed_test

// Пробы условий поверхности полосы X2 NTF-3 (проход «поверхность до кода»,
// Д114): значение формы subject не приводится к адресной форме и лежит в
// колонке адресата как есть; форма компонента и дефисная форма id судятся
// одной функцией формы субъекта; ключ окна «проект» берётся только из
// Values.Project и только id семейства проекта.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/ids"
	"github.com/PRO-Robotech/corelib/notify/feed"
)

func (f *fixture) recipientColumn(t *testing.T) []string {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT recipient_address FROM probe_notification_outbox ORDER BY enqueued_at`)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}
	require.NoError(t, rows.Err())
	return out
}

func (f *fixture) windowKeys(t *testing.T, scope string) []string {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT key FROM probe_notification_window WHERE scope = $1`, scope)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}
	require.NoError(t, rows.Err())
	return out
}

// Условие X2-3: строка формы subject — в колонке адресата user:<id> побайтно,
// ключ окна адресата — то же значение; близнец формы address — колонка несёт
// нормализованный адрес (адрес проходит address.Normalize, субъект — нет).
func TestNTF3_150_SubjectIsStoredAsTheSubject(t *testing.T) {
	f := newFixture(t, true)
	to := userSubject()
	require.NoError(t, f.put(t, formDesc(feed.RecipientSubject, perHour(5)), to, hello()))
	require.Equal(t, []string{to}, f.recipientColumn(t))
	require.Equal(t, []string{to}, f.windowKeys(t, "recipient"))

	g := newFixture(t, true)
	require.NoError(t, g.put(t, formDesc(feed.RecipientAddress), "A@Example.test", hello()))
	require.Equal(t, []string{"A@example.test"}, g.recipientColumn(t), "близнец: адрес нормализован")
}

// Условие X2-2: id пользователя в дефисной форме каталога ids принят; форма
// компонента system.<служба>-<роль> на месте id пользователя — отказ (её
// пропустила бы проверка «начинается с user:»).
func TestNTF3_150_SubjectFormIsJudgedByTheSubjectForm(t *testing.T) {
	f := newFixture(t, true)
	n, err := f.putRows(t, formDesc(feed.RecipientSubject), "user:"+ids.NewHyphenID(ids.PrefixUser), hello())
	require.NoError(t, err, "дефисная форма id пользователя")
	require.Equal(t, 1, n)

	g := newFixture(t, true)
	n, err = g.putRows(t, formDesc(feed.RecipientSubject), "user:system.vpc-reconciler", hello())
	require.ErrorIs(t, err, feed.ErrRecipientInvalid)
	require.NotContains(t, err.Error(), "vpc-reconciler", "текст отказа значения не несёт")
	require.Equal(t, 0, n)
}

// Условие X2-4: лимит на проект без id проекта, с id чужого семейства либо с
// «проектом» в атрибутах набора вместо Values.Project — ErrAttrsInvalid, строк
// 0. Близнец — id проекта в Values.Project: строка и ключ окна project — этот id.
func TestNTF3_14_ProjectKeyComesOnlyFromValuesProject(t *testing.T) {
	d := formDesc(feed.RecipientSubject, perHourProject(5))
	cases := map[string]feed.Values{
		"проекта нет":          hello(),
		"id чужого семейства":  inProject(ids.NewID(ids.PrefixVolume)),
		"проект атрибутом":     attrs("name", "probe", "project", ids.NewID("prj")),
		"проект без приставки": inProject("project-a"),
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, true)
			n, err := f.putRows(t, d, userSubject(), v)
			require.ErrorIs(t, err, feed.ErrAttrsInvalid)
			require.Equal(t, 0, n)
		})
	}
	t.Run("близнец Values.Project", func(t *testing.T) {
		f := newFixture(t, true)
		prj := ids.NewID("prj")
		n, err := f.putRows(t, d, userSubject(), inProject(prj))
		require.NoError(t, err)
		require.Equal(t, 1, n)
		require.Equal(t, []string{prj}, f.windowKeys(t, "project"))
	})
}
