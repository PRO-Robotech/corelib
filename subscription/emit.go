// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	subscriptionv1 "github.com/PRO-Robotech/corelib/api/corelib/subscription"
	"github.com/PRO-Robotech/corelib/journaltx"
	"github.com/PRO-Robotech/corelib/validate/nameform"
)

// ErrEntryRefused — запись журнала противоречит объявлению владельца: вид или
// род изменения вне словаря, вид без объявленных формы имени и якоря, пустой
// якорь проектного вида, якорь у вида уровня кластера, якорь записи расходится
// с тем, что выведет отображение, снятие вида с именем без годного имени.
// Отказ стоит ДО оператора вставки.
var ErrEntryRefused = errors.New("subscription: journal entry refused by the owner's declaration")

// ErrNotHelperTx — [Journal.Emit] позван не транзакцией, открытой
// `journaltx.Begin`: nil либо `journaltx.Tx`, собранный в обход помощника. У
// такой транзакции нет выставленного инициатора, и строка легла бы без него
// (или оператор упал бы на пустой транзакции). Отказ стоит до оператора.
var ErrNotHelperTx = errors.New("subscription: Journal.Emit needs a transaction opened by journaltx.Begin")

// Entry — одна строка журнала в словаре владельца.
type Entry struct {
	// Kind — вид предмета словом хранилища (ключ [Mapping.Kinds]).
	Kind string
	// ID — неизменяемый идентификатор предмета.
	ID string
	// ProjectID — проектный якорь; пуст у вида уровня кластера.
	ProjectID string
	// Change — род изменения словом владельца (ключ [Mapping.Changes]).
	Change string
	// Payload — полезная нагрузка строки. У снятия вида [NameFormDNS] несёт
	// снимок имени под ключом [NamePayloadKey].
	Payload map[string]any
}

// Emit — функция фундамента, пишущая строку журнала по дескриптору таблицы
// владельца: таблица и колонки — из [Storage], перевод вида и рода изменения,
// форма имени и якорь вида — из [Mapping] (NTF-3, З5).
//
// Транзакция — только транзакция помощника `journaltx`: колонка инициатора
// оператором не называется, её значение даёт умолчание колонки из настройки,
// выставленной [journaltx.Begin]. Колонка времени тоже не называется — её
// значение ставит база (`DEFAULT now()`). Иная транзакция — nil либо
// `journaltx.Tx`, собранный в обход Begin, — отвергается [ErrNotHelperTx].
//
// Запись, противоречащая объявлению, отвергается [ErrEntryRefused] до
// оператора; отказ называет вид (или род изменения), а не подставляет
// значение.
func (j Journal) Emit(ctx context.Context, tx *journaltx.Tx, e Entry) error {
	// Транзакцию помощника отличает выставленный инициатор: Begin без него
	// транзакцию не открывает, а собранный в обход Begin `journaltx.Tx` его
	// выставить не может (поле не экспортировано).
	if tx == nil || tx.Initiator() == "" {
		return ErrNotHelperTx
	}
	payload, err := j.admit(e)
	if err != nil {
		return err
	}

	st := j.Storage
	var q string
	args := []any{e.Kind, e.ID, e.Change, payload}
	if st.Project == ProjectInColumn {
		q = fmt.Sprintf(`INSERT INTO %s (%s, %s, %s, %s, %s) VALUES ($1, $2, $3, $4, $5)`,
			st.Table, st.KindColumn, st.IDColumn, st.ChangeColumn, st.PayloadColumn, st.ProjectColumn)
		args = append(args, e.ProjectID)
	} else {
		q = fmt.Sprintf(`INSERT INTO %s (%s, %s, %s, %s) VALUES ($1, $2, $3, $4)`,
			st.Table, st.KindColumn, st.IDColumn, st.ChangeColumn, st.PayloadColumn)
	}
	if _, err := tx.Exec(ctx, q, args...); err != nil {
		return fmt.Errorf("subscription: journal insert into %s: %w", st.Table, err)
	}
	return nil
}

// admit судит запись по объявлению и отдаёт полезную нагрузку в виде, в
// котором она ляжет в колонку. Имена таблицы и колонок судит [Journal.Validate]
// у конструктора сервера; здесь — только то, что зависит от записи.
func (j Journal) admit(e Entry) ([]byte, error) {
	if err := j.Storage.validate(); err != nil {
		return nil, err
	}
	kind, ok := j.Mapping.Kinds[e.Kind]
	if !ok {
		return nil, fmt.Errorf("%w: вид %q вне словаря владельца", ErrEntryRefused, e.Kind)
	}
	change, ok := j.Mapping.Changes[e.Change]
	if !ok {
		return nil, fmt.Errorf("%w: род изменения %q вне словаря владельца (вид %s)", ErrEntryRefused, e.Change, e.Kind)
	}
	if kind.NameForm == NameFormUnset || kind.Scope == ScopeUnset {
		return nil, fmt.Errorf("%w: вид %s не объявил NameForm и Scope — писать его функцией фундамента не по чему", ErrEntryRefused, e.Kind)
	}

	switch kind.Scope {
	case ScopeProject:
		if e.ProjectID == "" {
			return nil, fmt.Errorf("%w: пустой якорь у проектного вида %s", ErrEntryRefused, e.Kind)
		}
		if j.Storage.Project == ProjectAbsent {
			return nil, fmt.Errorf("%w: проектный вид %s в журнале без проектного измерения", ErrEntryRefused, e.Kind)
		}
	case ScopeCluster:
		if e.ProjectID != "" {
			return nil, fmt.Errorf("%w: якорь у вида уровня кластера %s", ErrEntryRefused, e.Kind)
		}
	default:
		return nil, fmt.Errorf("%w: вид %s: Scope = %d — такого состояния нет", ErrEntryRefused, e.Kind, kind.Scope)
	}

	if change == subscriptionv1.SubscriptionEvent_DELETED && kind.NameForm == NameFormDNS {
		name, ok := e.Payload[NamePayloadKey].(string)
		if !ok || !nameform.OK(name) {
			return nil, fmt.Errorf("%w: снятие вида %s без имени формы DNS-метки под ключом %q", ErrEntryRefused, e.Kind, NamePayloadKey)
		}
	}

	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return nil, fmt.Errorf("subscription: marshal payload of %s: %w", e.Kind, err)
	}
	if e.Payload == nil {
		payload = []byte("{}")
	}

	if j.Storage.Project == ProjectFromMapping && kind.Scope == ScopeProject {
		if j.Mapping.Anchor == nil {
			return nil, fmt.Errorf("%w: якорь журнала даёт отображение, но Mapping.Anchor не назван (вид %s)", ErrEntryRefused, e.Kind)
		}
		derived, err := j.Mapping.Anchor(Row{Kind: e.Kind, ID: e.ID, Change: e.Change, Payload: payload})
		if err != nil {
			return nil, fmt.Errorf("%w: якорь вида %s не выведен: %w", ErrEntryRefused, e.Kind, err)
		}
		if derived != e.ProjectID {
			return nil, fmt.Errorf("%w: якорь записи вида %s расходится с якорем, который выведет отображение", ErrEntryRefused, e.Kind)
		}
	}
	return payload, nil
}

// deletedName — имя на событии: только у снятия вида [NameFormDNS], из
// полезной нагрузки строки. Имя вне формы — жалоба, а не пустое значение под
// видом законного: такую строку записал не [Journal.Emit].
func deletedName(kind Kind, change subscriptionv1.SubscriptionEvent_Change, payload []byte) (name, complaint string) {
	if change != subscriptionv1.SubscriptionEvent_DELETED || kind.NameForm != NameFormDNS {
		return "", ""
	}
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		return "", "deleted row of a named kind carries an unreadable payload — the event goes without a name"
	}
	n, _ := m[NamePayloadKey].(string)
	if !nameform.OK(n) {
		return "", "deleted row of a named kind carries no DNS-label name — the event goes without a name"
	}
	return n, ""
}
