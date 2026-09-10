// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package quota_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/quota"
)

// Синхронизатор величин — порядок и отказоустойчивость.
//
// Задача `PRO-Robotech/kacho#410`.
//
// Здесь утверждается ПОРЯДОК прохода: прочитать курсор → применить страницу →
// сдвинуть курсор, и ни в каком другом сочетании. Что именно делают операторы с
// таблицей, утверждается интеграционной пробой владельца: подставная проекция
// про базу не знает и знать не должна.

// fakeSource — дельта, заданная страницами.
type fakeSource struct {
	pages  [][]quota.Change
	cursor []string
	calls  []string
	err    error
}

func (f *fakeSource) ListChangedSince(_ context.Context, cursor string, _ int32) ([]quota.Change, string, error) {
	f.calls = append(f.calls, cursor)
	if f.err != nil {
		return nil, "", f.err
	}
	idx := len(f.calls) - 1
	if idx >= len(f.pages) {
		// Догнали: пустая страница и ТОТ ЖЕ курсор. Владелец величин обязан
		// возвращать курсор всегда, включая пустую страницу.
		return nil, cursor, nil
	}
	return f.pages[idx], f.cursor[idx], nil
}

// fakeProjection записывает, что и в каком порядке с ней делали.
type fakeProjection struct {
	cursor   string
	applied  []quota.Change
	saved    []string
	rowsPer  int64
	order    []string
	applyErr error
	loadErr  error
	// passTaken — проход уже держит другая реплика. Дублёр обязан уметь
	// ОТКАЗАТЬ так же, как настоящая проекция: подставная, раздающая проход
	// всем, сделала бы невидимым ровно тот дефект, ради которого клейм заведён.
	passTaken bool
	// claims — сколько раз проход был взят.
	claims int
}

func (f *fakeProjection) ClaimPass(context.Context, time.Duration) (string, bool, error) {
	f.order = append(f.order, "claim")
	if f.loadErr != nil {
		return "", false, f.loadErr
	}
	if f.passTaken {
		return "", false, nil
	}
	f.claims++
	return f.cursor, true, nil
}

func (f *fakeProjection) ApplyChange(_ context.Context, ch quota.Change) (int64, error) {
	f.order = append(f.order, "apply")
	if f.applyErr != nil {
		return 0, f.applyErr
	}
	f.applied = append(f.applied, ch)
	return f.rowsPer, nil
}

func (f *fakeProjection) SaveCursor(_ context.Context, cursor string, _ int64) error {
	f.order = append(f.order, "save")
	f.saved = append(f.saved, cursor)
	f.cursor = cursor
	return nil
}

func (f *fakeProjection) Heartbeat(context.Context) error {
	f.order = append(f.order, "beat")
	return nil
}

func change(kind string, scope quota.Scope, scopeID string, value, rev int64) quota.Change {
	return quota.Change{Kind: kind, Scope: scope, ScopeID: scopeID, Value: value, Revision: rev}
}

func newSyncer(t *testing.T, src quota.Source, proj quota.Projection) *quota.Syncer {
	t.Helper()
	s, err := quota.NewSyncer(src, proj, quota.Config{}, slog.Default())
	require.NoError(t, err)
	return s
}

// TestSyncer_AppliesEveryPageAndAdvancesCursorAfterApplying — курсор двигается
// ПОСЛЕ применения, и страницы тянутся, пока он двигается.
//
// Порядок несущий: курсор, сохранённый до применения, потерял бы изменения при
// отказе — и потерял бы их молча, потому что следующий проход начал бы уже за
// ними.
func TestSyncer_AppliesEveryPageAndAdvancesCursorAfterApplying(t *testing.T) {
	src := &fakeSource{
		pages: [][]quota.Change{
			{change("vpc.network", quota.ScopeDefault, "", 32, 10)},
			{change("vpc.subnet", quota.ScopeProject, "prj-1", 8, 11)},
		},
		cursor: []string{"c1", "c2"},
	}
	proj := &fakeProjection{rowsPer: 3}

	rows, _, err := newSyncer(t, src, proj).RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(6), rows, "две страницы по три строки")

	require.Equal(t, []string{"claim", "apply", "save", "apply", "save", "beat"}, proj.order,
		"курсор двигается ПОСЛЕ применения своей страницы; отметка прохода — в конце")
	require.Equal(t, []string{"c1", "c2"}, proj.saved)
	require.Len(t, proj.applied, 2)
}

// TestSyncer_StopsWhenTheOwnerReturnsTheSameCursor — догнав, проход
// останавливается.
//
// Положительный контроль к пробе выше: без него «тянет, пока курсор двигается»
// было бы неотличимо от «тянет всегда», и синхронизатор крутил бы пустые
// страницы до предела.
func TestSyncer_StopsWhenTheOwnerReturnsTheSameCursor(t *testing.T) {
	src := &fakeSource{}
	proj := &fakeProjection{cursor: "c9"}

	rows, _, err := newSyncer(t, src, proj).RunOnce(context.Background())
	require.NoError(t, err)
	require.Zero(t, rows)
	require.Equal(t, []string{"c9"}, src.calls, "ровно один запрос: курсор не сдвинулся")
	require.Empty(t, proj.saved, "записывать нечего — курсор не переписывается впустую")
	require.Contains(t, proj.order, "beat", "но проход состоялся, и это обязано быть видно")
}

// TestSyncer_UnknownScopeStopsTheRunAndLeavesTheCursor — непонятое изменение
// НЕ пропускается.
//
// Пропуск здесь стоил бы дороже отказа: курсор уехал бы вперёд, строка осталась
// бы со старой величиной навсегда, а снаружи это выглядело бы как исправная
// синхронизация. Классификация чужого ответа без корзины «прочее».
func TestSyncer_UnknownScopeStopsTheRunAndLeavesTheCursor(t *testing.T) {
	src := &fakeSource{
		pages:  [][]quota.Change{{{Kind: "vpc.network", Scope: "REGION", ScopeID: "r", Revision: 5}}},
		cursor: []string{"c1"},
	}
	proj := &fakeProjection{}

	_, _, err := newSyncer(t, src, proj).RunOnce(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), `scope: unknown "REGION"`)
	require.Empty(t, proj.saved, "курсор не двигается: следующий проход встретит то же самое")
	require.Empty(t, proj.applied)
}

// TestSyncer_ApplyFailureLeavesTheCursorWhereItWas — отказ применения не
// продвигает курсор.
func TestSyncer_ApplyFailureLeavesTheCursorWhereItWas(t *testing.T) {
	src := &fakeSource{
		pages:  [][]quota.Change{{change("vpc.network", quota.ScopeDefault, "", 32, 10)}},
		cursor: []string{"c1"},
	}
	proj := &fakeProjection{applyErr: errors.New("peer refused")}

	_, _, err := newSyncer(t, src, proj).RunOnce(context.Background())
	require.Error(t, err)
	require.Empty(t, proj.saved)
}

// TestSyncer_RefusesToBeBuiltWithNothingToDo — собранный без источника или без
// проекции, синхронизатор был бы формой без содержания: исполнялся бы по
// расписанию и не делал ничего.
func TestSyncer_RefusesToBeBuiltWithNothingToDo(t *testing.T) {
	_, err := quota.NewSyncer(nil, &fakeProjection{}, quota.Config{}, nil)
	require.Error(t, err)

	_, err = quota.NewSyncer(&fakeSource{}, nil, quota.Config{}, nil)
	require.Error(t, err)

	// Законный близнец: с обоими — собирается.
	_, err = quota.NewSyncer(&fakeSource{}, &fakeProjection{}, quota.Config{}, nil)
	require.NoError(t, err)
}

// TestChange_ValidateNamesTheFieldItRefuses — отказ называет поле.
func TestChange_ValidateNamesTheFieldItRefuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		ch   quota.Change
		want string
	}{
		{"без вида", quota.Change{Scope: quota.ScopeDefault, Revision: 1}, "kind: required"},
		{"неизвестная область", quota.Change{Kind: "k", Scope: "X", Revision: 1}, `scope: unknown "X"`},
		{"область без объекта", quota.Change{Kind: "k", Scope: quota.ScopeProject, Revision: 1}, "scope_id: required"},
		{"ревизия не назначена", quota.Change{Kind: "k", Scope: quota.ScopeDefault}, "revision: must be positive"},
		{"отрицательная величина", quota.Change{Kind: "k", Scope: quota.ScopeDefault, Revision: 1, Value: -1}, "value: must not be negative"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorContains(t, tc.ch.Validate(), tc.want)
		})
	}

	// Положительный контроль: законное изменение проходит — иначе проба выше
	// зеленела бы и на реализации, отвергающей всё подряд.
	require.NoError(t, change("vpc.network", quota.ScopeDefault, "", 16, 4).Validate())
	require.NoError(t, quota.Change{
		Kind: "vpc.network", Scope: quota.ScopeAccount, ScopeID: "acc-1", Revision: 9, Withdrawn: true,
	}.Validate())
}

// TestScope_PrecedenceIsAnOrderAndNotAnOpinion — старшинство областей.
//
// Правило одно и живёт в одном месте; проба закрепляет именно отношение, потому
// что применяется оно и в Go, и в предикате оператора, и разойтись эти два
// применения не имеют права.
func TestScope_PrecedenceIsAnOrderAndNotAnOpinion(t *testing.T) {
	require.True(t, quota.ScopeProject.AtLeast(quota.ScopeAccount))
	require.True(t, quota.ScopeAccount.AtLeast(quota.ScopeDefault))
	require.True(t, quota.ScopeProject.AtLeast(quota.ScopeDefault))

	require.False(t, quota.ScopeDefault.AtLeast(quota.ScopeAccount))
	require.False(t, quota.ScopeAccount.AtLeast(quota.ScopeProject))

	require.True(t, quota.ScopeDefault.AtLeast(quota.ScopeDefault), "область не ниже самой себя")

	require.False(t, quota.Scope("REGION").Valid())
	require.True(t, quota.ScopeAccount.Valid())
}
