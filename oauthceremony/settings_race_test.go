// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// settings_race_test.go — одновременные обмены на свежей церемонии не делят
// изменяемого состояния движка.
//
// Предмет — обычный рабочий случай, а не повтор: несколько клиентов сразу
// после старта службы одновременно меняют каждый СВОЙ код. Первый же обмен
// доходит до сверки секрета клиента, и если хешер секрета в настройках движка
// не назван, движок заводит его ленивой записью — из того исполнителя, который
// пришёл первым, без синхронизации с остальными.
//
// Сила пробы — под -race (конвейер гонит `go test ./... -race`): без детектора
// она проверяет лишь, что каждый обмен прошёл. Класс «метод настроек пишет в
// них» судится без детектора и без одновременности двумя внутренними пробами:
// замену значения поля ловит TestEngineSettingsBuiltByNewAreOnlyReadOnTheRequestPath,
// запись в содержимое поля в известных формах —
// TestEngineSettingsMethodsWriteNoFieldContent. Слепую зону её разбора
// (blindZoneForms) держит только эта проба — и только на пути обмена.
package oauthceremony_test

import (
	"context"
	"sync"
	"testing"

	"github.com/PRO-Robotech/corelib/oauthceremony"
)

// TestConcurrentExchangesOnAFreshCeremonyShareNoEngineState — N кодов выданы
// последовательно (точка авторизации секрета клиента не сверяет), затем N
// обменов стартуют одновременно по одному сигналу. Каждый обмен обязан пройти.
func TestConcurrentExchangesOnAFreshCeremonyShareNoEngineState(t *testing.T) {
	const exchanges = 4

	store := newMemoryPorts()
	registerTestClient(t, store)
	ceremony := newTestCeremony(t, store.ports())

	codes := make([]string, exchanges)
	for i := range codes {
		codes[i], _ = issueCode(t, ceremony)
	}

	start := make(chan struct{})
	tokens := make([]oauthceremony.TokenResult, exchanges)
	errs := make([]error, exchanges)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			tokens[i], errs[i] = ceremony.Exchange(context.Background(), codeExchange(codes[i]))
		}()
	}
	close(start)
	wg.Wait()

	for i := range codes {
		if errs[i] != nil {
			t.Errorf("обмен №%d отвергнут: %v", i+1, errs[i])
			continue
		}
		if tokens[i].AccessToken == "" {
			t.Errorf("обмен №%d прошёл без токена доступа", i+1)
		}
	}
}
