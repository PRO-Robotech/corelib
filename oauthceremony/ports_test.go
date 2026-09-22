// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// ports_test.go — пробы КОНТРАКТА ПОРТА: числа затронутых строк и срока
// вызова.
//
// Каждая проба подменяет исход ОДНОГО метода порта и смотрит, чем это
// оборачивается снаружи. Проверяется не то, что реализация умеет отвечать
// правильно, а то, что НЕПРАВИЛЬНЫЙ ответ виден и назван своим случаем.
package oauthceremony_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/oauthceremony"
)

// TestStoreOutcomeZeroValueIsNotDeclared — нулевое значение типа исходом не
// является.
//
// Это основание всего контракта: реализация, забывшая назвать число, не может
// случайно сойти за реализацию, честно сообщившую ноль.
func TestStoreOutcomeZeroValueIsNotDeclared(t *testing.T) {
	var zero oauthceremony.StoreOutcome
	if zero.Declared() {
		t.Fatal("нулевое значение StoreOutcome названо объявленным исходом")
	}
	if zero.Rows() != 0 {
		t.Errorf("нулевое значение отдаёт %d строк", zero.Rows())
	}

	for _, rows := range []int64{-1, 0, 1, 2, 1 << 40} {
		outcome := oauthceremony.RowsTouched(rows)
		if !outcome.Declared() {
			t.Errorf("RowsTouched(%d) дал необъявленный исход", rows)
		}
		if outcome.Rows() != rows {
			t.Errorf("RowsTouched(%d) отдаёт %d строк", rows, outcome.Rows())
		}
	}
}

// exchangeAfterConsumeOutcome прогоняет обмен кода, подменив исход погашения.
func exchangeAfterConsumeOutcome(t *testing.T, outcome func(string) (oauthceremony.StoreOutcome, error)) error {
	t.Helper()

	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	code, _ := issueCode(t, ceremony)
	store.consumeOverride = outcome

	_, err := ceremony.Exchange(context.Background(), oauthceremony.TokenRequest{
		Grant:        oauthceremony.GrantAuthorizationCode,
		ClientID:     testClientID,
		ClientSecret: testSecret,
		AuthMethod:   oauthceremony.ClientAuthBasic,
		Code:         code,
		RedirectURI:  testRedirectURI,
		CodeVerifier: testVerifier,
	})
	return err
}

// TestUndeclaredOutcomeFromConsumeIsAContractBreach — порт, вернувший
// StoreOutcome{}, назван нарушителем контракта, а НЕ принят за «ноль строк».
//
// Разница существенна: приняв необъявленный исход за ноль, церемония ответила
// бы «код уже погашен» — то есть превратила бы дефект реализации в
// правдоподобный отказ протокола, который никто не пойдёт разбирать.
func TestUndeclaredOutcomeFromConsumeIsAContractBreach(t *testing.T) {
	err := exchangeAfterConsumeOutcome(t, func(string) (oauthceremony.StoreOutcome, error) {
		return oauthceremony.StoreOutcome{}, nil
	})
	if err == nil {
		t.Fatal("обмен прошёл при необъявленном исходе погашения")
	}
	if !errors.Is(err, oauthceremony.ErrPortContract) {
		t.Fatalf("случай %v, ожидался %v", oauthceremony.CodeOf(err), oauthceremony.CodePortContract)
	}
	if errors.Is(err, oauthceremony.ErrAuthorizationCodeConsumed) {
		t.Error("необъявленный исход выдан за «код уже погашен»")
	}
}

// TestSeveralRowsFromConsumeIsAContractBreach — подпись обязана быть
// уникальной; две затронутые строки означают, что это не так.
func TestSeveralRowsFromConsumeIsAContractBreach(t *testing.T) {
	err := exchangeAfterConsumeOutcome(t, func(string) (oauthceremony.StoreOutcome, error) {
		return oauthceremony.RowsTouched(2), nil
	})
	if !errors.Is(err, oauthceremony.ErrPortContract) {
		t.Fatalf("случай %v, ожидался %v", oauthceremony.CodeOf(err), oauthceremony.CodePortContract)
	}
}

// TestNegativeRowCountIsAContractBreach — драйвер, не умеющий считать строки,
// возвращает −1. Принять это за ноль значило бы принять «не знаю» за «ничего
// не затронуто».
func TestNegativeRowCountIsAContractBreach(t *testing.T) {
	err := exchangeAfterConsumeOutcome(t, func(string) (oauthceremony.StoreOutcome, error) {
		return oauthceremony.RowsTouched(-1), nil
	})
	if !errors.Is(err, oauthceremony.ErrPortContract) {
		t.Fatalf("случай %v, ожидался %v", oauthceremony.CodeOf(err), oauthceremony.CodePortContract)
	}
}

// TestZeroRowsFromConsumeMeansAlreadyRedeemed — честный ноль означает
// проигранную гонку, и это НЕ нарушение контракта.
//
// Проба-близнец к трём предыдущим: она отличается ровно одним фактом —
// исход объявлен. Без неё прибор был бы зелёным и у реализации, называющей
// нарушением всё подряд.
func TestZeroRowsFromConsumeMeansAlreadyRedeemed(t *testing.T) {
	err := exchangeAfterConsumeOutcome(t, func(string) (oauthceremony.StoreOutcome, error) {
		return oauthceremony.RowsTouched(0), nil
	})
	if !errors.Is(err, oauthceremony.ErrAuthorizationCodeConsumed) {
		t.Fatalf("случай %v, ожидался %v",
			oauthceremony.CodeOf(err), oauthceremony.CodeAuthorizationCodeConsumed)
	}
	if errors.Is(err, oauthceremony.ErrPortContract) {
		t.Error("честный ноль назван нарушением контракта")
	}
}

// TestUndeclaredOutcomeFromStoreIsAContractBreach — то же правило на пути
// записи: вставка, не назвавшая числа, обязана быть видна.
func TestUndeclaredOutcomeFromStoreIsAContractBreach(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	store.storeOverride = func(string) (oauthceremony.StoreOutcome, error) {
		return oauthceremony.StoreOutcome{}, nil
	}

	intent, err := ceremony.Authorize(context.Background(), authorizeRequest())
	if err != nil {
		t.Fatalf("Authorize отказал: %v", err)
	}
	_, err = ceremony.CompleteAuthorization(context.Background(), intent, oauthceremony.AuthorizationGrant{
		Subject:       testSubject,
		GrantedScopes: []string{"openid", "offline"},
	})
	if !errors.Is(err, oauthceremony.ErrPortContract) {
		t.Fatalf("случай %v, ожидался %v", oauthceremony.CodeOf(err), oauthceremony.CodePortContract)
	}
}

// TestInsertThatTouchedNoRowIsAContractBreach — вставка, ничего не
// вставившая, — дефект, а не «ноль строк».
func TestInsertThatTouchedNoRowIsAContractBreach(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	store.storeOverride = func(string) (oauthceremony.StoreOutcome, error) {
		return oauthceremony.RowsTouched(0), nil
	}

	intent, err := ceremony.Authorize(context.Background(), authorizeRequest())
	if err != nil {
		t.Fatalf("Authorize отказал: %v", err)
	}
	_, err = ceremony.CompleteAuthorization(context.Background(), intent, oauthceremony.AuthorizationGrant{
		Subject:       testSubject,
		GrantedScopes: []string{"openid", "offline"},
	})
	if !errors.Is(err, oauthceremony.ErrPortContract) {
		t.Fatalf("случай %v, ожидался %v", oauthceremony.CodeOf(err), oauthceremony.CodePortContract)
	}
}

// TestPortDeadlineIsItsOwnCase — у каждого вызова порта свой срок, и его
// истечение имеет СВОЙ случай.
//
// Без отдельного случая зависшее хранилище было бы неотличимо от
// «внутренней ошибки», и дежурный искал бы дефект в коде вместо базы.
func TestPortDeadlineIsItsOwnCase(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports(), func(cfg *oauthceremony.Config) {
		cfg.PortTimeout = 20 * time.Millisecond
		cfg.OperationTimeout = 5 * time.Second
	})

	store.lookupDelay = 400 * time.Millisecond

	_, err := ceremony.Authorize(context.Background(), authorizeRequest())
	if err == nil {
		t.Fatal("запрос прошёл при зависшем справочнике клиентов")
	}
	if !errors.Is(err, oauthceremony.ErrPortDeadline) {
		t.Fatalf("случай %v, ожидался %v", oauthceremony.CodeOf(err), oauthceremony.CodePortDeadline)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Error("наш отказ перестал совпадать с часовым стандартной библиотеки")
	}
	if errors.Is(err, oauthceremony.ErrServerError) {
		t.Error("истёкший срок вызова порта назван внутренней ошибкой")
	}
}

// TestBeginInheritsOperationDeadline — ПРЕДИКАТ, названный в доке BeginTX.
//
// Открытие единицы работы — единственный вызов порта БЕЗ собственного срока:
// свой срок сделал бы возвращаемый контекст потомком умирающего. Взамен его
// ограничивает срок ВСЕЙ операции, и здесь это замеряется, а не заявляется:
// зависшее открытие обязано кончиться отказом, а не держать запрос.
func TestBeginInheritsOperationDeadline(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)

	ports := store.ports()
	ports.Transaction = &recordingUnitOfWork{hang: true}

	// Срок порта равен сроку операции — нижняя допустимая граница. Она
	// выбрана намеренно: при ней ЕДИНСТВЕННОЕ, что может остановить
	// зависшее открытие, — срок операции, потому что своего у открытия нет.
	ceremony := newTestCeremony(t, ports, func(cfg *oauthceremony.Config) {
		cfg.PortTimeout = 150 * time.Millisecond
		cfg.OperationTimeout = 150 * time.Millisecond
	})

	code, _ := issueCode(t, ceremony)

	started := time.Now()
	_, err := ceremony.Exchange(context.Background(), oauthceremony.TokenRequest{
		Grant:        oauthceremony.GrantAuthorizationCode,
		ClientID:     testClientID,
		ClientSecret: testSecret,
		AuthMethod:   oauthceremony.ClientAuthBasic,
		Code:         code,
		RedirectURI:  testRedirectURI,
		CodeVerifier: testVerifier,
	})
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("обмен прошёл при зависшем открытии единицы работы")
	}
	if elapsed > time.Second {
		t.Errorf("церемония держалась %v при сроке операции 150ms", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("отказ не совпал с часовым стандартной библиотеки: %v", err)
	}
	if !errors.Is(err, oauthceremony.ErrPortDeadline) {
		t.Errorf("случай %v, ожидался %v", oauthceremony.CodeOf(err), oauthceremony.CodePortDeadline)
	}
}

// TestUnitOfWorkIsEngagedWhenNamed — названная единица работы ДЕЙСТВИТЕЛЬНО
// участвует в обмене кода, а не просто лежит в наборе.
//
// Проба нужна потому, что движок выясняет наличие транзакций утверждением
// типа: мост без неё и мост с ней — РАЗНЫЕ типы, и ошибка в выборе была бы
// молчаливой.
func TestUnitOfWorkIsEngagedWhenNamed(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)

	unit := &recordingUnitOfWork{}
	ports := store.ports()
	ports.Transaction = unit

	ceremony := newTestCeremony(t, ports)
	code, _ := issueCode(t, ceremony)

	if _, err := ceremony.Exchange(context.Background(), oauthceremony.TokenRequest{
		Grant:        oauthceremony.GrantAuthorizationCode,
		ClientID:     testClientID,
		ClientSecret: testSecret,
		AuthMethod:   oauthceremony.ClientAuthBasic,
		Code:         code,
		RedirectURI:  testRedirectURI,
		CodeVerifier: testVerifier,
	}); err != nil {
		t.Fatalf("Exchange отказал: %v", err)
	}

	begun, committed, rolled := unit.counts()
	if begun == 0 {
		t.Error("единица работы названа, но обмен кода её не открыл")
	}
	if committed == 0 {
		t.Error("единица работы открыта, но не закреплена")
	}
	if rolled != 0 {
		t.Errorf("успешный обмен откатил единицу работы %d раз", rolled)
	}
	t.Logf("единица работы: открытий %d шт, закреплений %d шт, откатов %d шт", begun, committed, rolled)
}

// TestRotatedRefreshTokenWithoutItsGrantIsAContractBreach — выборка, назвавшая
// токен обёрнутым, обязана отдать и его грант: по нему отзывается семейство.
// Без гранта отзывать нечего, и это дефект порта, а не «повтор» — иначе
// церемония ответила бы «семейство отозвано», не отозвав ничего.
//
// Близнец — TestSequentialRefreshReplayRevokesTheFamily: тот же исход выборки
// с грантом даёт случай повтора (отличие — идентификатор гранта в записи).
func TestRotatedRefreshTokenWithoutItsGrantIsAContractBreach(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	first := exchangeCode(t, ceremony)
	store.fetchRefreshOverride = func(string) (oauthceremony.GrantRecord, error) {
		return oauthceremony.GrantRecord{ClientID: testClientID}, oauthceremony.ErrRefreshTokenRotated
	}

	_, err := ceremony.Exchange(context.Background(), refreshRequest(first.RefreshToken))
	if !errors.Is(err, oauthceremony.ErrPortContract) {
		t.Fatalf("случай %v, ожидался %v", oauthceremony.CodeOf(err), oauthceremony.CodePortContract)
	}
	if errors.Is(err, oauthceremony.ErrRefreshTokenRotated) {
		t.Error("обёрнутый токен без гранта выдан за повтор, после которого семейство отозвано")
	}
}

// TestCallerCancellationIsItsOwnCase — снятие вызывающим отличимо от
// истёкшего срока.
func TestCallerCancellationIsItsOwnCase(t *testing.T) {
	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	store.lookupDelay = 400 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := ceremony.Authorize(ctx, authorizeRequest())
	if err == nil {
		t.Fatal("снятый запрос прошёл")
	}
	if !errors.Is(err, oauthceremony.ErrPortCanceled) {
		t.Fatalf("случай %v, ожидался %v", oauthceremony.CodeOf(err), oauthceremony.CodePortCanceled)
	}
	if errors.Is(err, oauthceremony.ErrPortDeadline) {
		t.Error("снятие вызывающим названо истёкшим сроком")
	}
}
