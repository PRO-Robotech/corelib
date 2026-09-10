// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package quota

// start_authority_internal_test.go — заведение тянущего стоит под ОБЪЯВЛЕНИЕМ
// домена величин, а не под наличием соединения соседа.
//
// Ребро одно, полос у него две: разрешение величины на пути запроса и фоновая
// дельта. Вторая опаснее, потому что её отказ ФАТАЛЕН ПРИ СБОРКЕ: пока подъём
// стоял под наличием соединения соседа, снятие авторитета величин означало, что
// пять чужих служб не поднимаются вовсе. Приёмка KAN-QUOTA-1, сценарии
// KAN-Q1-06 и KAN-Q4-13.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// recordingExecer — носитель, запоминающий ИСПОЛНЕННЫЕ операторы.
//
// Дублёр не снисходительнее настоящего в том, что важно пробе: он ничего не
// придумывает и не отвечает «хорошо» на операторы, которых не было.
type recordingExecer struct {
	stmts []string
	// args запоминаются намеренно: наблюдаемое состояние курсора живёт в
	// ЗНАЧЕНИИ, а не в тексте оператора. Проба, сверяющая только текст, зеленела
	// бы на реализации, которая пишет одно и то же при любом объявлении.
	args []any
	err  error
}

func (r *recordingExecer) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	r.stmts = append(r.stmts, sql)
	r.args = append(r.args, args...)
	return pgconn.CommandTag{}, r.err
}

// QueryRow отдаёт «строки нет». Предмет проб этого файла — какие операторы
// исполняет ПОДЪЁМ, а не как идёт проход; отказ клейма означает «проход держит
// другая реплика» — штатный исход, а не поблажка: дублёр здесь не принимает
// ничего, чего не принял бы настоящий.
func (r *recordingExecer) QueryRow(context.Context, string, ...any) pgx.Row { return noRow{} }

type noRow struct{}

func (noRow) Scan(...any) error { return pgx.ErrNoRows }

func (r *recordingExecer) touched(fragment string) bool {
	for _, s := range r.stmts {
		if strings.Contains(s, fragment) {
			return true
		}
	}
	return false
}

func absent(t *testing.T) Authority {
	t.Helper()
	a, err := ResolveAuthority(Declaration{Knob: "quota.authority", Value: NotDeployed})
	require.NoError(t, err)
	return a
}

func present(t *testing.T) Authority {
	t.Helper()
	a, err := ResolveAuthority(Declaration{
		Knob: "quota.authority", Value: "kaname-internal:9091",
		TransportKnob: "mtls.quota_authority", TransportDeclared: true,
	})
	require.NoError(t, err)
	return a
}

// TestStartLimitSync_KAN_Q1_06_AbsentAuthorityStartsNoPuller — при «не развёрнут»
// тянущий не заводится ВОВСЕ: ни одного обращения к соседу.
func TestStartLimitSync_KAN_Q1_06_AbsentAuthorityStartsNoPuller(t *testing.T) {
	db := &recordingExecer{}
	src := &budgetSource{}
	_, logger := captureLogger()

	stop, err := StartLimitSync(context.Background(), db, absent(t), nil, "kacho_vpc", Config{}, logger)
	require.NoError(t, err, "процесс обязан ПОДНЯТЬСЯ на законном объявлении «не развёрнут»")
	require.NotNil(t, stop)
	stop()

	require.Zero(t, src.calls, "к снятому соседу не обращаются ни разу")
	require.False(t, db.touched("applied_rows_total = applied_rows_total"),
		"накопительный счётчик строк не двигается")
	require.False(t, db.touched("pulls_total = pulls_total"),
		"накопительный счётчик проходов не двигается")
	require.False(t, db.touched("SET cursor ="), "курсор не двигается")
}

// TestStartLimitSync_KAN_Q1_06_AbsentAuthorityIsNamedInTheCursor — состояние
// курсора НАЗЫВАЕТ причину, а не показывает застой.
//
// После снятия авторитета «ни строки не применено» становится штатным и вечным.
// Сигнал застоя, оставленный как есть, срабатывал бы всегда — а проверку,
// кричащую на нормальной работе, перестают читать вместе с настоящими находками.
func TestStartLimitSync_KAN_Q1_06_AbsentAuthorityIsNamedInTheCursor(t *testing.T) {
	db := &recordingExecer{}
	_, logger := captureLogger()

	_, err := StartLimitSync(context.Background(), db, absent(t), nil, "kacho_vpc", Config{}, logger)
	require.NoError(t, err)

	require.True(t, db.touched("authority_state"),
		"объявление домена величин обязано доехать до курсора; исполнено: %v", db.stmts)
	require.False(t, db.touched("updated_at = now()"),
		"отметка занятости прохода не трогается: иначе первый проход после "+
			"возврата авторитета откладывается на срок аренды")
}

// TestStartLimitSync_PresentAuthorityIsNamedTooPositiveTwin — положительный близнец.
//
// Без него утверждение «состояние названо» зеленело бы на реализации, которая
// пишет одно и то же при любом объявлении.
func TestStartLimitSync_PresentAuthorityIsNamedTooPositiveTwin(t *testing.T) {
	dbAbsent, dbPresent := &recordingExecer{}, &recordingExecer{}
	_, logger := captureLogger()

	_, err := StartLimitSync(context.Background(), dbAbsent, absent(t), nil, "kacho_vpc", Config{}, logger)
	require.NoError(t, err)
	stop, err := StartLimitSync(context.Background(), dbPresent, present(t), &budgetSource{}, "kacho_vpc", Config{}, logger)
	require.NoError(t, err)
	stop()

	require.Contains(t, dbAbsent.args, string(AuthorityAbsent))
	require.Contains(t, dbPresent.args, string(AuthorityPresent))
	require.NotEqual(t, dbAbsent.args, dbPresent.args,
		"два объявления обязаны давать РАЗНОЕ наблюдаемое состояние курсора")
}

// TestStartLimitSync_DeployedWithoutSourceRefusesAtAssembly — объявлен адрес,
// а источника нет: отказ ПРИ СБОРКЕ.
//
// Иначе собранный подъём исполнялся бы по расписанию и не делал ничего,
// оставаясь на вид работающим, — ровно тот класс, который этот механизм чинит.
func TestStartLimitSync_DeployedWithoutSourceRefusesAtAssembly(t *testing.T) {
	_, logger := captureLogger()
	_, err := StartLimitSync(context.Background(), &recordingExecer{}, present(t), nil, "kacho_vpc", Config{}, logger)
	require.Error(t, err)
	require.Contains(t, err.Error(), "quota.authority")
}

// TestStartLimitSync_AbsentWithSourceRefusesAtAssembly — зеркало: объявлено
// «не развёрнут», а потребитель всё-таки дозвонился.
//
// Расхождение объявления и проводки — не мелочь: оно означает, что решение
// принято дважды и в двух местах, и второе место невидимо оператору.
func TestStartLimitSync_AbsentWithSourceRefusesAtAssembly(t *testing.T) {
	_, logger := captureLogger()
	_, err := StartLimitSync(context.Background(), &recordingExecer{}, absent(t), &budgetSource{}, "kacho_vpc", Config{}, logger)
	require.Error(t, err)
	require.Contains(t, err.Error(), NotDeployed)
}

// TestStartLimitSync_CursorRecordFailureIsFatal — не записали состояние ⇒ не поднялись.
//
// Состояние курсора и есть то, чем «домен снят» отличается от «тянущий умер».
// Подняться, не сумев его назвать, значит завести ровно ту неразличимость,
// ради устранения которой оно заводится.
func TestStartLimitSync_CursorRecordFailureIsFatal(t *testing.T) {
	_, logger := captureLogger()
	db := &recordingExecer{err: errors.New("нет соединения с базой")}
	_, err := StartLimitSync(context.Background(), db, absent(t), nil, "kacho_vpc", Config{}, logger)
	require.Error(t, err)
}
