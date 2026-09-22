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
//
// # Второе, что несёт ведомость: семейство, которое обязано умереть
//
// Мост, увидевший повтор токена обновления, записывает сюда грант, чьё
// семейство отзывается. Отзыв исполняет ЦЕРЕМОНИЯ по завершении операции, а
// не мост и не движок, потому что ни у одного из них для этого нет места:
//
//   - на одновременном повторе движок узнаёт о нём по нулю строк оборота
//     ВНУТРИ своей единицы работы и откатывает её (`flow_refresh.go`,
//     handleRefreshTokenEndpointStorageError); отзыв, исполненный мостом в той
//     же единице работы, откатился бы вместе с ней;
//   - на последовательном повторе движок отзывает сам, но его исход после
//     отзыва огрубляется, и отказ отзыва был бы неотличим от успеха.
//
// Церемония отзывает ВНЕ единицы работы движка и называет отказ отзыва
// отказом операции — а не «повтор», за которым живое семейство.
type operationNotes struct {
	mu      sync.Mutex
	precise *ProtocolError
	// replayedFamily — семейство, у которого предъявлен обёрнутый токен
	// обновления. Нулевое значение — повтора не было.
	replayedFamily replayedFamily
}

// replayedFamily — грант повторённого токена и клиент, которому грант выдан.
//
// Клиент нужен ровно одному пути — отзыву (RFC 7009 §2.1): отозвать семейство
// по обёрнутому токену вправе только клиент, которому грант выдан. На пути
// обмена клиент не нужен: повтор там — повтор, кем бы он ни был предъявлен, и
// движок отзывает семейство раньше, чем сверит клиента. Пусто — клиент не
// назван: так пишет оборот, у которого есть только грант.
type replayedFamily struct {
	grantID  string
	clientID string
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

// markReplayedFamily записывает грант, чьё семейство обязано быть отозвано.
// Пустой идентификатор сюда не доходит: мост отвергает его раньше как
// нарушение контракта порта (отозвать семейство без имени нечем).
func (n *operationNotes) markReplayedFamily(grantID, clientID string) {
	if n == nil || grantID == "" {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.replayedFamily.grantID == "" {
		n.replayedFamily = replayedFamily{grantID: grantID, clientID: clientID}
	}
}

// replayed отдаёт семейство, у которого замечен повтор; нулевое значение —
// повтора не было.
func (n *operationNotes) replayed() replayedFamily {
	if n == nil {
		return replayedFamily{}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.replayedFamily
}

// coarsenable — случаи, которые мост заносит в ведомость.
//
// Перечень ЗАКРЫТ и невелик намеренно: заноси мы всякий отказ хранилища,
// ведомость подменяла бы вердикт движка и там, где вердикт точнее. Здесь
// только те случаи, которые (а) строго точнее любого протокольного вердикта и
// (б) означают, что операция не могла завершиться успехом.
func coarsenable(code FailureCode) bool {
	switch code {
	case CodeAuthorizationCodeConsumed, CodeRefreshTokenRotated,
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
