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
// Записывается ПЕРВЫЙ случай: первый отказ ХРАНИЛИЩА в разборе движка и есть
// причина, а последующие — уже следствия разбора. Отказ ОТЗЫВА семейства
// через ведомость не идёт вовсе: его церемония возвращает прямо, отказом
// операции (см. ниже), и записанный случай повтора его не заслоняет.
//
// # Второе, что несёт ведомость: семейство, которое обязано умереть
//
// Мост, увидевший повтор — кода авторизации или токена обновления, —
// записывает сюда грант, чьё семейство отзывается. Отзыв исполняет ЦЕРЕМОНИЯ
// по завершении операции, а не мост и не движок, потому что ни у одного из них
// для этого нет места:
//
//   - на одновременном повторе движок узнаёт о нём по нулю строк погашения
//     кода или оборота токена ВНУТРИ своей единицы работы и откатывает её
//     (`flow_authorize_code_token.go`, `flow_refresh.go`); отзыв, исполненный
//     мостом в той же единице работы, откатился бы вместе с ней, а без
//     единицы работы движок на этом исходе не отзывает ничего;
//   - на последовательном повторе движок отзывает сам, но его исход после
//     отзыва огрубляется, и отказ отзыва был бы неотличим от успеха.
//
// Церемония отзывает ВНЕ единицы работы движка и называет отказ отзыва
// отказом операции — а не «повтор», за которым живое семейство.
//
// # Третье: код, предъявленный и погашенный в этой операции
//
// Ноль строк погашения приходит мосту БЕЗ гранта (порт гасит по подписи), а
// движок спрашивает привязку PKCE отдельно от кода. Выборка кода в той же
// операции стоит раньше обоих, и её запись мост заносит сюда: по её гранту
// повтор узнаётся на нуле строк погашения, а привязку PKCE движок получает из
// неё же — из записи кода, а не из второго хранилища.
//
// Погашение кода мост тоже заносит сюда: код гасится РОВНО ОДИН РАЗ за обмен —
// при снятии привязки PKCE, — и выборка выдачи, нашедшая код погашенным ЭТОЙ
// ЖЕ операцией, повтором не является, как и погашение на выдаче, которому
// остаётся лишь это признать.
//
// # Четвёртое: почему в этой операции отзывают
//
// Порт отзыва получает причину (RevocationReason), а отзывают в операции двое
// — движок и церемония, — и движок причины не передаёт: его хранилище
// отзывает по одному идентификатору запроса. Причина берётся отсюда, и
// источников у неё ровно два:
//
//   - операция называет её сама — отзыв, о котором просит клиент (Revoke);
//   - мост замечает повтор кода или токена обновления и записывает её вместе с
//     семейством.
//
// Названная операцией побеждает: клиент, отзывающий прежним, уже обёрнутым
// токеном обновления, просит отзыва, хотя мост и замечает повтор. Нет ни
// одного источника — отзыва в операции быть не должно, и мост отказывает, не
// позвав порта (см. revocationReason).
type operationNotes struct {
	mu      sync.Mutex
	precise *ProtocolError
	// replayedFamily — семейство, у которого замечен повтор. Нулевое
	// значение — повтора не было.
	replayedFamily replayedFamily
	// presented — код, предъявленный в этой операции. Нулевое значение —
	// кода не предъявляли.
	presented presentedCode
	// consumed — подпись кода, погашенного этой операцией. Пусто — операция
	// кода не гасила.
	consumed string
	// requested — причина отзыва, названная самой операцией. Нулевое
	// значение — операция причины не называет.
	requested RevocationReason
}

// replayedFamily — грант повторённого артефакта, клиент, которому грант
// выдан, и случай, которым повтор отвечается.
//
// Клиент нужен ровно одному пути — отзыву (RFC 7009 §2.1): отозвать семейство
// по обёрнутому токену вправе только клиент, которому грант выдан. На пути
// обмена клиент не нужен: повтор там — повтор, кем бы он ни был предъявлен, и
// движок отзывает семейство раньше, чем сверит клиента. Пусто — клиент не
// назван: так пишет оборот, у которого есть только грант.
//
// refusal — отказ, которым церемония отвечает, если движок сам обмен НЕ
// отверг: выданное таким обменом принадлежит отозванному семейству.
//
// reason — причина отзыва по этому повтору: повтор кода или повтор токена
// обновления. Её называет тот, кто повтор заметил, вместе с отказом.
type replayedFamily struct {
	grantID  string
	clientID string
	refusal  *ProtocolError
	reason   RevocationReason
}

// presentedCode — код, выбранный в этой операции: подпись и запись кода
// такой, какой её отдало хранилище.
type presentedCode struct {
	signature string
	record    AuthorizationCodeRecord
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

// markReplayedFamily записывает грант, чьё семейство обязано быть отозвано,
// случай, которым отвечается повтор, и причину отзыва. Пустой идентификатор
// сюда не доходит: мост отвергает его раньше как нарушение контракта порта
// (отозвать семейство без имени нечем).
func (n *operationNotes) markReplayedFamily(grantID, clientID string, refusal *ProtocolError, reason RevocationReason) {
	if n == nil || grantID == "" {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.replayedFamily.grantID == "" {
		n.replayedFamily = replayedFamily{grantID: grantID, clientID: clientID, refusal: refusal, reason: reason}
	}
}

// requestRevocation записывает причину отзыва, названную самой операцией.
func (n *operationNotes) requestRevocation(reason RevocationReason) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.requested = reason
}

// revocationReason отдаёт причину отзыва в этой операции: названную ею самой,
// а если она не названа — причину замеченного повтора. Ложь — причины нет ни
// у операции, ни у повтора (или ведомости нет вовсе): отзыв в такой операции —
// дефект провязки, и порт отзыва звать нельзя.
func (n *operationNotes) revocationReason() (RevocationReason, bool) {
	if n == nil {
		return "", false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	reason := n.requested
	if !reason.Declared() {
		reason = n.replayedFamily.reason
	}
	return reason, reason.Declared()
}

// notePresentedCode записывает код, выбранный в этой операции.
func (n *operationNotes) notePresentedCode(code presentedCode) {
	if n == nil || code.signature == "" {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.presented = code
}

// presentedCodeOf отдаёт код, выбранный в этой операции под подписью
// signature; ложь — такого кода в операции не выбирали (или ведомости нет).
func (n *operationNotes) presentedCodeOf(signature string) (presentedCode, bool) {
	if n == nil {
		return presentedCode{}, false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.presented.signature == "" || n.presented.signature != signature {
		return presentedCode{}, false
	}
	return n.presented, true
}

// noteConsumed записывает, что код под подписью signature погасила эта
// операция.
func (n *operationNotes) noteConsumed(signature string) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.consumed = signature
}

// consumedHere отвечает, погасила ли код под подписью signature эта операция.
// Без ведомости — нет: мосту, вызванному вне операции, признать погашение
// своим нечем.
func (n *operationNotes) consumedHere(signature string) bool {
	if n == nil {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return signature != "" && n.consumed == signature
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
