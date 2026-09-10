// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// noticethreshold_integration_test.go — уведомление, поднятое ПОСЛЕ того, как
// цепочка заглушила порог сессии, всё равно доезжает до оператора.
//
// # Почему цепочка синтетическая, а производитель — настоящий
//
// Класс производит ОДИН файл дерева, и он лежит в ЧУЖОМ Go-модуле
// (`services/iam`): собрать его цепочку отсюда нельзя. Поэтому проба сперва
// требует, чтобы производитель в дереве БЫЛ и нёс ровно тот оператор, ради
// которого она написана, — а затем воспроизводит этот ОДИН факт двухшаговой
// цепочкой. Уедет производитель — проба откажет и назовёт причину, а не
// позеленеет на утверждении, которому нечего утверждать.
//
// # Две полосы, и вторая — не украшение симметрии
//
// Положительная: цепочка глушит порог, следующая миграция говорит — оператор
// обязан это увидеть. Законный близнец: порог поднял САМ ОПЕРАТОР (параметром
// подключения) — тогда молчание законно, и накат не вправе его перебивать.
// Без близнеца проба зеленела бы и на подстановке `SET … = notice`, которая
// решает за оператора.
package migratorcli_test

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/migratorcli"
	"github.com/PRO-Robotech/corelib/pgtest"
)

const (
	// noticeMuteProducer — файл, глушивший порог сессии, названный координатой,
	// чтобы проба ИСТЕКАЛА вместе с ним.
	//
	// ОН ИСТЁК. Координата вела в применённую миграцию службы доступа, а служба
	// вынесена отдельным продуктом; в этом дереве оператора глушения не
	// производит НИ ОДИН файл (предикат: `git grep -l client_min_messages --
	// '*.sql'` → 0).
	//
	// Проба при этом остаётся, и это не послабление, а следствие того, ЧЬЁ
	// свойство она судит. Предмет — поведение ФУНДАМЕНТА: цепочка, чей шаг
	// заглушил порог, обязана получить его обратно. Фундамент здесь, он
	// публикуется, и теперь его потребитель — в том числе то самое дерево, где
	// живёт производитель. Снять пробу значило бы перестать судить живое
	// свойство ровно тогда, когда цена его отказа выросла.
	//
	// Что изменилось честно: факт глушения проба ВОСПРОИЗВОДИТ САМА, а не берёт
	// у производителя. Предпосылка ниже это ОБЪЯВЛЯЕТ и самовосстанавливается:
	// появится в дереве файл, глушащий порог, — она снова сверит оператор с ним,
	// без чьей-либо памяти.
	noticeMuteProducer = "services/iam/internal/migrations/0001_initial.sql"
	// noticeMuteStatement — сам оператор. Проба сверяет ЕГО: файл мог остаться,
	// а оператор из него уйти.
	noticeMuteStatement = "SET client_min_messages = warning;"
	// noticeChainText — то, что обязан увидеть оператор.
	noticeChainText = "census: rows migrated 0"
	// noticeThresholdParamName — имя порога. Второй редакции этого имени в пробе
	// нет: она сверяет ровно тот параметр, который возвращает накат.
	noticeThresholdParamName = "client_min_messages"
)

// noticeRepoRoot — корень репозитория: каталог, где лежит корневой go.mod.
func noticeRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqualf(t, parent, dir,
			"go.mod не найден выше %s — корень репозитория не определён", dir)
		dir = parent
	}
}

// requireMuteProducerStillExists — предпосылка проверяется, а не подразумевается.
//
// Исходов ДВА, и оба объявляются вслух:
//
//   - производитель В ДЕРЕВЕ ЕСТЬ — оператор сверяется с ним дословно, как
//     прежде: фикстура обязана воспроизводить ФАКТ, а не выдуманное;
//   - производителя нет — проба воспроизводит факт САМА, и это печатается.
//     Молчаливый пропуск здесь был бы хуже отказа: он неотличим от сверки.
func requireMuteProducerStillExists(t *testing.T) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(noticeRepoRoot(t), filepath.FromSlash(noticeMuteProducer)))
	if err != nil {
		t.Logf("производитель %s в этом дереве отсутствует (%v): оператор %q фикстура "+
			"воспроизводит САМА. Свойство фундамента — «цепочка получает порог обратно» "+
			"— судится по-прежнему; сверка оператора с производителем НЕ ИСПОЛНЯЕТСЯ и "+
			"вернётся вместе с ним", noticeMuteProducer, err, noticeMuteStatement)
		return
	}
	require.Containsf(t, string(b), noticeMuteStatement,
		"в %s больше нет оператора %q — производителя класса нет, и фикстура ниже "+
			"воспроизводит выдуманное. Назовите другого производителя либо снимите пробу "+
			"вместе с предметом", noticeMuteProducer, noticeMuteStatement)
}

// noticeMutingChain — цепочка из двух шагов: первый глушит порог сессии ровно
// тем оператором, что стоит у производителя; второй говорит.
func noticeMutingChain(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mute := "-- +goose Up\n" + noticeMuteStatement + "\n\n-- +goose Down\nSELECT 1;\n"
	speak := "-- +goose Up\n-- +goose StatementBegin\nDO $$ BEGIN\n" +
		"  RAISE NOTICE '" + noticeChainText + "';\nEND $$;\n-- +goose StatementEnd\n" +
		"\n-- +goose Down\nSELECT 1;\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "00001_mute.sql"), []byte(mute), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "00002_speak.sql"), []byte(speak), 0o600))
	return dir
}

// applyNoticeChain применяет цепочку тем же соединением, каким её применяет
// накат, и возвращает всё, что сервер сказал оператору.
func applyNoticeChain(t *testing.T, dsn, chainDir string) string {
	t.Helper()
	var out bytes.Buffer
	relay := migratorcli.NewNoticeRelay("noticethreshold", &out)

	require.NoError(t, migratorcli.SetupGoose(os.DirFS(chainDir), migratorcli.SpecPostgres))
	goose.SetLogger(goose.NopLogger())

	ctx := context.Background()
	db, err := migratorcli.OpenDB(ctx, dsn, migratorcli.SpecPostgres, relay)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	require.NoError(t, goose.UpContext(ctx, db, "."), "цепочка обязана накатываться")
	relay.WriteCensus()
	t.Logf("перепись: доставлено %d, отброшено %d\n--- сказано сервером ---\n%s",
		relay.Delivered(), relay.Suppressed(), out.String())
	return out.String()
}

// withOperatorThreshold — DSN, в котором порог назвал САМ ОПЕРАТОР.
func withOperatorThreshold(t *testing.T, dsn, value string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	require.NoErrorf(t, err, "DSN стенда неразбираем: %q", dsn)
	q := u.Query()
	q.Set(noticeThresholdParamName, value)
	u.RawQuery = q.Encode()
	return u.String()
}

// TestTheChainGetsItsNoticeThresholdBack — ПОЛОЖИТЕЛЬНАЯ половина.
func TestTheChainGetsItsNoticeThresholdBack(t *testing.T) {
	if testing.Short() {
		t.Skip("накат идёт против живой базы (testcontainers): под кратким режимом пропускается")
	}
	requireMuteProducerStillExists(t)

	said := applyNoticeChain(t, pgtest.NewEmptyDB(t), noticeMutingChain(t))

	require.Containsf(t, said, noticeChainText,
		"оператор НЕ ВИДИТ того, что сервер сказал ПОСЛЕ заглушившей порог миграции: "+
			"в выводе нет %q. `SET %s` без `LOCAL` держится до конца сессии, и обработчик "+
			"уведомлений тут бессилен — сообщение не отправлено вовсе (#2560)",
		noticeChainText, noticeThresholdParamName)
	require.Containsf(t, said, "NOTICE: ",
		"вывод не называет УРОВЕНЬ сообщения: без него оператор не отличит уведомление "+
			"от отказа.\n%s", said)
}

// TestTheOperatorsOwnThresholdIsNotOverridden — ЗАКОННЫЙ БЛИЗНЕЦ.
//
// Порог поднят самим оператором параметром подключения. Возврат обязан вернуть
// ЕГО величину, а не подставить свою: иначе накат решает за оператора, а проба
// выше зеленела бы на подстановке.
func TestTheOperatorsOwnThresholdIsNotOverridden(t *testing.T) {
	if testing.Short() {
		t.Skip("накат идёт против живой базы (testcontainers): под кратким режимом пропускается")
	}
	requireMuteProducerStillExists(t)

	dsn := withOperatorThreshold(t, pgtest.NewEmptyDB(t), "warning")
	said := applyNoticeChain(t, dsn, noticeMutingChain(t))

	require.NotContainsf(t, said, noticeChainText,
		"накат перебил выбор оператора: порог %s = warning он назвал САМ, а уведомление "+
			"всё равно доставлено. Возврат обязан возвращать величину оператора, а не "+
			"подставлять свою.\n%s", noticeThresholdParamName, said)
	require.NotContainsf(t, said, "NOTICE: ",
		"на поднятом оператором пороге не должно быть НИ ОДНОГО уведомления этого уровня.\n%s", said)
	// Перепись печатается и здесь: «ноль доставленных» обязано быть отличимо от
	// «обработчика не было вовсе» — иначе близнец молчал бы по неверной причине.
	require.Containsf(t, said, "migration-notices noticethreshold:",
		"переписи в выводе нет: молчание близнеца неотличимо от отсутствия обработчика.\n%s", said)
	// Строка, которую видит оператор при осмысленном молчании.
	require.Contains(t, said, "0 delivered")
}
