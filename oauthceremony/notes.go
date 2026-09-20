// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package oauthceremony

import (
	"context"
	"sync"
)

// operationNotes — ведомость точных случаев, замеченных мостом за одну
// операцию.
//
// # Зачем она нужна
//
// Измерено на внесённом движке (`internal/oauth2/handler/oauth2/
// flow_authorize_code_token.go`, ревизия 3726c54, строки 33–52): обнаружив
// повторно предъявленный код, движок отзывает артефакты гранта и возвращает
// СВЕЖИЙ `ErrInvalidGrant` — не оборачивая ту ошибку, которую вернуло
// хранилище. Цепочка `Unwrap` обрывается, и наш точный случай «код уже
// погашен» до вызывающего не доезжает: снаружи он неотличим от «грант
// недействителен» вообще.
//
// В соседней ветке (там же, строка 167) движок оборачивает: `ErrServerError.
// WithWrap(err)`, и случай доезжает. То есть точность отказа зависела бы от
// того, какая из двух проверок сработала первой, — а это разные проверки
// ОДНОГО события.
//
// Ведомость убирает эту разницу: мост записывает в неё случаи, которые движок
// вправе огрубить, а церемония, получив отказ, предпочитает записанное.
//
// # Чего она НЕ делает
//
// Не подменяет успешный исход. Ведомость читается ТОЛЬКО тогда, когда
// операция и так завершилась отказом: запись о неудачном чтении, после
// которого движок нашёл артефакт в другом месте, ни на что не влияет.
//
// Записывается ПЕРВЫЙ случай: первый отказ хранилища и есть причина, а
// последующие — уже следствия разбора.
type operationNotes struct {
	mu      sync.Mutex
	precise *ProtocolError
}

type operationNotesKey struct{}

// withNotes заводит ведомость на одну операцию.
func withNotes(ctx context.Context) (context.Context, *operationNotes) {
	notes := &operationNotes{}
	return context.WithValue(ctx, operationNotesKey{}, notes), notes
}

// notesFrom достаёт ведомость. Её отсутствие законно: мост может вызываться
// и вне операции церемонии (например, из пробы), и тогда записывать некуда.
func notesFrom(ctx context.Context) *operationNotes {
	notes, ok := ctx.Value(operationNotesKey{}).(*operationNotes)
	if !ok {
		return nil
	}
	return notes
}

// record заносит случай, если ведомость есть и ещё пуста.
func (n *operationNotes) record(p *ProtocolError) {
	if n == nil || p == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.precise == nil {
		n.precise = p
	}
}

// preferRecorded выбирает, что отдать наружу: записанный точный случай или
// вердикт движка.
//
// Записанное предпочитается, потому что оно ПРИЧИНА, а вердикт движка —
// следствие: движок отказал именно потому, что хранилище ответило так. Если
// ведомость пуста, отдаётся вердикт.
func (n *operationNotes) preferRecorded(engineVerdict error) error {
	if n == nil {
		return engineVerdict
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.precise == nil {
		return engineVerdict
	}
	return n.precise
}

// coarsenable — случаи, которые мост заносит в ведомость.
//
// Перечень ЗАКРЫТ и невелик намеренно: заноси мы всякий отказ хранилища,
// ведомость подменяла бы вердикт движка и там, где вердикт точнее. Здесь
// только те случаи, которые (а) строго точнее любого протокольного вердикта и
// (б) означают, что операция не могла завершиться успехом.
func coarsenable(code FailureCode) bool {
	switch code {
	case CodeAuthorizationCodeConsumed, CodeAssertionReplayed,
		CodePortContract, CodePortDeadline, CodePortCanceled:
		return true
	default:
		return false
	}
}

// note записывает случай в ведомость операции, если он из перечня.
func note(ctx context.Context, p *ProtocolError) *ProtocolError {
	if p != nil && coarsenable(p.Code) {
		notesFrom(ctx).record(p)
	}
	return p
}
