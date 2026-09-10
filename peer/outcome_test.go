// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Внешний тест-пакет: носитель проверяется ровно с той поверхности, которую
// видит сервис. Проба, написанная внутри пакета, утверждала бы о полосе больше,
// чем о ней может узнать вызывающий.
package peer_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	kerrors "github.com/PRO-Robotech/corelib/errors"
	"github.com/PRO-Robotech/corelib/peer"
)

// reasonOf достаёт машинный признак из собранного ответа. Именно он, а не проза,
// — то, на что ключуется клиент, поэтому пробы утверждают ЕГО, а не текст.
func reasonOf(t *testing.T, err error) string {
	t.Helper()
	st, ok := status.FromError(err)
	require.Truef(t, ok, "ответ носителя обязан быть gRPC-статусом, получено %T", err)
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return info.GetReason()
		}
	}
	return ""
}

func metaOf(t *testing.T, err error) map[string]string {
	t.Helper()
	st, ok := status.FromError(err)
	require.True(t, ok)
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return info.GetMetadata()
		}
	}
	return nil
}

// Классификация закрыта: у КАЖДОГО кода gRPC есть исход, и ни один не попадает в
// корзину «прочее» молча — непонятый ответ имеет собственное имя.
//
// Перепись идёт по всем кодам, какие бывают, а не по списку тех, что мы ждали:
// иначе проба молчала бы ровно о коде, которого автор не вспомнил.
func TestEveryGrpcCodeGetsANamedOutcome(t *testing.T) {
	classified := peer.ClassifiedCodes()

	var named, unclassified int
	for c := codes.OK; c <= codes.Unauthenticated; c++ {
		got := peer.Classify(status.Error(c, "peer said so"))
		want, known := classified[c]
		if known {
			require.Equalf(t, want, got, "код %s классифицирован не своей полосой", c)
			named++
			continue
		}
		require.Equalf(t, peer.OutcomeUnclassified, got,
			"код %s не назван в каноне, но и не попал в «не смог классифицировать» —\n"+
				"    значит у него появилась молчаливая полоса", c)
		unclassified++
	}
	require.NotZero(t, named, "предмет пробы исчез: каноничных кодов ноль")
	require.NotZero(t, unclassified,
		"ни одного кода вне канона — проба про непонятый ответ потеряла предмет,\n"+
			"    и «корзины прочее нет» больше ничем не подтверждается")
	t.Logf("перепись: кодов осмотрено %d (каноничных %d, вне канона %d)",
		named+unclassified, named, unclassified)
}

// Единственная временная полоса — недоступность. Утверждение перечисляет ВЕСЬ
// набор исходов, а не только те, что помнил автор: исход, добавленный завтра,
// обязан явно объявить свою временность здесь.
func TestOnlyUnavailableIsTransient(t *testing.T) {
	transient := map[peer.Outcome]bool{peer.OutcomeUnavailable: true}

	for _, o := range peer.AllOutcomes() {
		require.Equalf(t, transient[o], o.Transient(),
			"полоса %s объявила временность %v — а повтор идентичного запроса на ней %v",
			o, o.Transient(), transient[o])
	}
	require.False(t, peer.OutcomeDenied.Transient(),
		"отказ в правах объявлен временным: повтор не пройдёт никогда, а голова партиции\n"+
			"    будет держаться всё окно повторов — очередь при этом выглядит живой")
	require.False(t, peer.OutcomeUnclassified.Transient(),
		"непонятый ответ объявлен временным — это и есть корзина «прочее», спрятанная в политику")
	t.Logf("перепись: исходов осмотрено %d", len(peer.AllOutcomes()))
}

// Забытая классификация не имеет права выглядеть успехом: нулевое значение —
// непонятый ответ.
func TestZeroOutcomeIsNotSuccess(t *testing.T) {
	var zero peer.Outcome
	require.Equal(t, peer.OutcomeUnclassified, zero)
	require.False(t, zero.Transient())
	require.False(t, zero.Reason().IsDeclared())
	require.Equal(t, codes.Internal,
		status.Code(zero.Status(peer.Ref{Service: "vpc"}, peer.Prose{Missing: "%s not found"})))
}

// Код и машинный признак берутся у полосы вместе. Проба идёт по всем исходам,
// поэтому новый исход без объявленной пары краснеет здесь.
func TestCodeAndReasonComeFromTheSameLane(t *testing.T) {
	want := map[peer.Outcome]struct {
		code   codes.Code
		reason string
	}{
		peer.OutcomeMissing:      {codes.FailedPrecondition, "PEER_RESOURCE_MISSING"},
		peer.OutcomeDenied:       {codes.FailedPrecondition, "PEER_RESOURCE_MISSING"},
		peer.OutcomeMalformed:    {codes.FailedPrecondition, "PEER_RESOURCE_MISSING"},
		peer.OutcomeStateRefused: {codes.FailedPrecondition, "PEER_RESOURCE_STATE"},
		peer.OutcomeUnavailable:  {codes.Unavailable, "PEER_UNAVAILABLE"},
	}

	ref := peer.Ref{Service: "vpc", ResourceType: "geo.zone", ResourceID: "zone-a"}
	var checked int
	for _, o := range peer.AllOutcomes() {
		w, ok := want[o]
		if !ok {
			// OK и непонятый ответ полосы контракта не имеют — это утверждается
			// отдельно, здесь они пропускаются осознанно.
			require.Falsef(t, o.Reason().IsDeclared(),
				"полоса %s получила признак контракта, но в таблице ответов её нет", o)
			continue
		}
		checked++
		err := o.Status(ref, peer.Prose{
			Missing:     "unknown zone id '%s'",
			State:       "zone %s is not usable",
			Unavailable: "geo zone validation unavailable",
		})
		require.Equalf(t, w.code, status.Code(err), "полоса %s отдала не свой код", o)
		require.Equalf(t, w.reason, reasonOf(t, err), "полоса %s отдала не свой машинный признак", o)
		require.Equalf(t, "zone-a", metaOf(t, err)["resource_id"],
			"полоса %s потеряла идентификатор в метаданных", o)
	}
	require.Equal(t, len(want), checked)
	t.Logf("перепись: полос с ответом контракта осмотрено %d из %d исходов", checked, len(peer.AllOutcomes()))
}

// Отказ в правах наружу неотличим от промаха — это анти-оракул, а не небрежность.
func TestDeniedIsIndistinguishableOutsideButNotInside(t *testing.T) {
	ref := peer.Ref{Service: "nlb", ResourceType: "vpc.subnet", ResourceID: "sub-1"}
	prose := peer.Prose{Missing: "subnet %s not found"}
	denied := peer.OutcomeDenied.Status(ref, prose)
	missing := peer.OutcomeMissing.Status(ref, prose)

	require.Equal(t, status.Code(missing), status.Code(denied))
	require.Equal(t, reasonOf(t, missing), reasonOf(t, denied))
	require.Equal(t, status.Convert(missing).Message(), status.Convert(denied).Message(),
		"по тексту отличают «нет доступа» от «не существует» — это оракул существования")

	require.NotEqual(t, peer.OutcomeMissing, peer.OutcomeDenied,
		"внутри полосы обязаны различаться: у них разные решения о повторе и разная наблюдаемость")
}

// Проза, утверждающая отсутствие НЕНАЗВАННОГО ресурса, невыразима.
//
// Проба парная: отрицание («пустой идентификатор не даёт „subnet  not found“»)
// без положительного контроля зеленело бы и на носителе, который не собирает
// ответ вовсе.
func TestEmptyIdCannotProduceAbsenceProse(t *testing.T) {
	form := peer.Prose{Missing: "subnet %s not found"}

	lost := peer.OutcomeMissing.Status(peer.Ref{Service: "nlb", ResourceType: "vpc.subnet"}, form)
	msg := status.Convert(lost).Message()
	require.NotContains(t, msg, "subnet  not found",
		"проза утверждает отсутствие того, чего вызывающий не называл")
	require.Contains(t, msg, "reference is empty",
		"пустая ссылка обязана называть себя пустой ссылкой, а не отсутствующим ресурсом")
	require.NotContains(t, metaOf(t, lost), "resource_id",
		"пустой идентификатор уехал в метаданные — ключ с пустым значением читается\n"+
			"    как «идентификатор известен и пуст»")

	named := peer.OutcomeMissing.Status(
		peer.Ref{Service: "nlb", ResourceType: "vpc.subnet", ResourceID: "sub-7"}, form)
	require.Equal(t, "subnet sub-7 not found", status.Convert(named).Message(),
		"законный близнец обязан отдавать контракт-прозу дословно")
}

// Намеренная непрозрачность — отдельный вызов, и она не называет идентификатор
// ни в тексте, ни в метаданных.
func TestOpaqueStatusNamesNothing(t *testing.T) {
	err := peer.OutcomeMissing.Status(
		peer.Ref{Service: "nlb", ResourceType: "vpc.address", ResourceID: "addr-1"},
		peer.Prose{Missing: "Illegal argument addressId", Opaque: true})
	require.Equal(t, "Illegal argument addressId", status.Convert(err).Message())
	require.Equal(t, "PEER_RESOURCE_MISSING", reasonOf(t, err))
	require.NotContains(t, metaOf(t, err), "resource_id",
		"непрозрачная полоса назвала идентификатор в метаданных — раскрытие через заднюю дверь")
}

// Непонятый отказ не притворяется полосой контракта и не тащит наружу прозу
// соседа.
func TestUnclassifiedNeitherRetriesNorLeaks(t *testing.T) {
	peerErr := status.Error(codes.Internal, "pq: connection to 10.4.3.2:5432 failed")
	got := peer.Classify(peerErr)
	require.Equal(t, peer.OutcomeUnclassified, got)
	require.False(t, got.Transient())

	out := got.Status(peer.Ref{Service: "compute", ResourceType: "storage.volume", ResourceID: "vol-1"},
		peer.Prose{Missing: "volume %s not found"})
	require.Equal(t, codes.Internal, status.Code(out))
	require.NotContains(t, status.Convert(out).Message(), "10.4.3.2")
	require.NotContains(t, status.Convert(out).Message(), "vol-1",
		"проза вызывающего описывала полосу, о которой ничего не установлено")
	require.Empty(t, reasonOf(t, out), "непонятый отказ выдал себя за полосу контракта")
}

// Полоса переживает обёртку контекстом: клиент, добавивший «geo zone get %q: %w»,
// не теряет классификацию.
func TestClassificationSurvivesWrapping(t *testing.T) {
	wrapped := fmt.Errorf("geo zone get %q: %w", "zone-a", status.Error(codes.NotFound, "Zone zone-a not found"))
	require.Equal(t, peer.OutcomeMissing, peer.Classify(wrapped))
}

// Свой срок и своя отмена — тоже «ответа, на который можно опереться, нет».
func TestOwnDeadlineAndCancelAreUnavailable(t *testing.T) {
	require.Equal(t, peer.OutcomeUnavailable, peer.Classify(context.DeadlineExceeded))
	require.Equal(t, peer.OutcomeUnavailable, peer.Classify(context.Canceled))
	require.Equal(t, peer.OutcomeUnavailable,
		peer.Classify(fmt.Errorf("zone get: %w", context.DeadlineExceeded)))
}

// Не-статус вовсе полосы не получает: назначать повтор чужой ошибке мы не вправе.
func TestNonStatusErrorIsUnclassified(t *testing.T) {
	require.Equal(t, peer.OutcomeUnclassified, peer.Classify(errors.New("boom")))
	require.Equal(t, peer.OutcomeOK, peer.Classify(nil))
}

// Словарь полос, который читает носитель, — тот же, что объявлен фундаментом.
// Без этого утверждения носитель мог бы отвечать полосой, снятой с контракта.
func TestCarrierUsesTheDeclaredDictionary(t *testing.T) {
	declared := map[string]bool{}
	for _, r := range kerrors.AllReasons() {
		declared[r.Token()] = true
	}
	var used int
	for _, o := range peer.AllOutcomes() {
		r := o.Reason()
		if !r.IsDeclared() {
			continue
		}
		require.Truef(t, declared[r.Token()], "полоса %s отвечает токеном %q вне словаря", o, r.Token())
		used++
	}
	t.Logf("перепись: токенов словаря %d, использовано носителем %d", len(declared), used)
}

// «Владелец установил, что ссылка не годится» — предикат для вызывающего, чей
// ответ собирается не здесь. Перепись идёт по ВСЕМУ набору исходов: новый исход
// без объявленной стороны краснеет тут, а не расходится по вызывающим.
func TestRefusedReferenceSplitsTheClosedSet(t *testing.T) {
	refused := map[peer.Outcome]bool{
		peer.OutcomeMissing:      true,
		peer.OutcomeStateRefused: true,
		peer.OutcomeDenied:       true,
		peer.OutcomeMalformed:    true,
	}
	for _, o := range peer.AllOutcomes() {
		require.Equalf(t, refused[o], o.RefusedReference(),
			"полоса %s сменила сторону предиката — это правка контракта вызывающих", o)
	}
	require.False(t, peer.OutcomeUnavailable.RefusedReference(),
		"перебой у соседа выдаётся за установленный отказ — временное станет терминальным")
	require.False(t, peer.OutcomeUnclassified.RefusedReference(),
		"непонятый ответ выдаётся за установленный отказ — мы утверждаем за владельца то,\n"+
			"    чего он не говорил")
	require.Equal(t, len(peer.AllOutcomes()), len(refused)+3,
		"набор исходов изменился — перепись предиката больше не покрывает его целиком")
	t.Logf("перепись: исходов %d, установленный отказ у %d", len(peer.AllOutcomes()), len(refused))
}

// Полоса недоступности не называет чужой ресурс — и её текст доезжает целиком.
//
// Носитель подставлял идентификатор в ЛЮБУЮ прозу, а проза недоступности глагола
// не несёт (так объявлено в [peer.Prose] и так написаны все три её объявления в
// дереве). Лишний аргумент печатается форматтером как `%!(EXTRA string=<id>)` —
// то есть арендатор получал служебный мусор в контрактном тексте, а
// идентификатор чужого ресурса всё-таки называл, вопреки замыслу полосы.
//
// Прежняя редакция проб этого не ловила: `TestCodeAndReasonComeFromTheSameLane`
// утверждает код, признак и метаданные — сообщение не утверждает ни одна.
func TestUnavailableProseCarriesNoResourceVerb(t *testing.T) {
	ref := peer.Ref{Service: "nlb", ResourceType: "geo.region", ResourceID: "reg-1"}
	form := peer.Prose{Missing: "Region %s not found", Unavailable: "region lookup unavailable"}

	err := peer.OutcomeUnavailable.Status(ref, form)
	msg := status.Convert(err).Message()
	require.Equal(t, "region lookup unavailable", msg,
		"текст полосы недоступности изменён носителем")
	require.NotContains(t, msg, "reg-1",
		"полоса недоступности назвала чужой ресурс, о котором ничего не установила")

	// Законный близнец: полоса, которая ресурс НАЗЫВАЕТ, по-прежнему его называет —
	// иначе отрицание выше зеленело бы на носителе, разучившемся подставлять вовсе.
	named := status.Convert(peer.OutcomeMissing.Status(ref, form)).Message()
	require.Equal(t, "Region reg-1 not found", named)
}

// Пустая ссылка не превращает недоступность в утверждение о ссылке.
//
// Ветка «ссылка пуста» принадлежит полосам, которые чужой ресурс называют. У
// недоступности утверждения о ссылке нет вовсе: сосед не ответил, и что там за
// идентификатор — не установлено. «region reference is empty» было бы вторым
// ложным утверждением поверх первого.
func TestUnavailableSaysNothingAboutTheReference(t *testing.T) {
	err := peer.OutcomeUnavailable.Status(
		peer.Ref{Service: "storage", ResourceType: "geo.zone"},
		peer.Prose{Missing: "unknown zone id '%s'", Unavailable: "geo zone validation unavailable"})
	require.Equal(t, "geo zone validation unavailable", status.Convert(err).Message())

	// Законный близнец: у полосы промаха пустая ссылка по-прежнему называет себя.
	lost := peer.OutcomeMissing.Status(
		peer.Ref{Service: "storage", ResourceType: "geo.zone"},
		peer.Prose{Missing: "unknown zone id '%s'"})
	require.Contains(t, status.Convert(lost).Message(), "reference is empty")
}

// Нейтральная форма недоступности — тоже без глагола: полоса, у которой своей
// прозы нет, обязана ответить честным текстом, а не служебным мусором.
func TestNeutralUnavailableProseIsAlsoVerbless(t *testing.T) {
	err := peer.OutcomeUnavailable.Status(
		peer.Ref{Service: "vpc", ResourceType: "iam.project", ResourceID: "prj-9"}, peer.Prose{})
	msg := status.Convert(err).Message()
	require.Equal(t, "iam.project lookup unavailable", msg)
	require.NotContains(t, msg, "prj-9")
}
