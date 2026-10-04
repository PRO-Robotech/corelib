// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package feed — лента почтовых извещений у источника (NTF-1, З6–З12, З27):
// постановка Put в транзакции вызывающего, окно лимита, флаг источника;
// сервер ленты Claim/Ack (Server), уборщик истечения и уборка закрытых строк
// и прошедших окон (StartSweeper, RetentionSubjects), кольцо ключей секрета
// (Keyring), словарь исходов и метрики ленты.
//
// Порядок внутри Put несущий (З7): всё, что может отвергнуть вызов, стоит до
// первого оператора SQL либо выражено нулём строк условного оператора под
// точкой сохранения, и транзакция вызывающего после любого сторожа пригодна к
// коммиту. Рантайм источника не тянет notify/spec (NTF1-D05).
package feed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/PRO-Robotech/corelib/auth"
	"github.com/PRO-Robotech/corelib/ids"
	"github.com/PRO-Robotech/corelib/journaltx"
	"github.com/PRO-Robotech/corelib/notify/address"
	"github.com/PRO-Robotech/corelib/notify/feed/internal/tablename"
	"github.com/PRO-Robotech/corelib/notify/form"
	"github.com/PRO-Robotech/corelib/operations"
)

// Values — набор значений постановки. Attrs — значения атрибутов в Go-форме
// form.Presence (string; у timestamp — time.Time или написание ленты); ключ
// незаданного optional в набор не попадает (З5). Initiator — ключ окна
// области initiator; нужен, когда у шаблона есть лимит на инициатора.
// Project — ключ окна области project (NTF-3 Р14): id проекта, который
// заполняет код источника из своей записи; нужен, когда у шаблона есть лимит
// на проект. Атрибут набора ключом окна не служит.
type Values struct {
	Initiator string
	Project   string
	Attrs     map[string]any
}

// limitedPutSetting — транзакционная отметка «постановка с лимитами уже была»
// (УК85). Живёт внутри точки сохранения Put и откатывается вместе с ней.
const limitedPutSetting = "kacho_feed.limited_put"

// savepointRollbackTimeout — свой срок отката к точке сохранения после отказа
// оператора постановки (arch-per-call-deadline). Откат отвязан от отмены
// вызывающего — транзакция вызывающего обязана остаться пригодной к его
// решению, — но не от предела: на зависшем соединении он возвращает ошибку не
// позже этого срока. ROLLBACK TO SAVEPOINT — оператор без чтения данных.
const savepointRollbackTimeout = 5 * time.Second

// Queued — исход постановки PutID: id записанной строки ленты либо его
// отсутствие. Отсутствие выражено типом, а не пустой строкой на месте id:
// флаг выключен и класс notice — строки нет, ID() → "", false; сторож и
// ошибка хранилища — нулевое значение рядом с ошибкой.
type Queued struct {
	id     string
	queued bool
}

// ID — id строки ленты, записанной в транзакции вызывающего, и признак, что
// строка записана. Строка видна другим после коммита этой транзакции; откат
// снимает её вместе с id.
func (q Queued) ID() (string, bool) { return q.id, q.queued }

// PutID ставит письмо шаблона desc адресату to в транзакции вызывающего tx
// (З7) и отвечает id записанной строки — тем, что вставил оператор
// постановки, а не прочитанным из ленты. Источник берётся из контекста
// (Source.Bind); без него — ErrSourceUnbound.
//
// Исходы: (id, nil) — строка, вклад в окна и строка журнала подписки записаны;
// (без id, nil) — флаг выключен и класс notice, не записано ничего; иначе
// без id и один из пяти сторожей PutGuards с именем шаблона или атрибута,
// либо ошибка хранилища. После сторожа транзакция пригодна к коммиту;
// ErrSecondLimitedPut — дефект вызывающего, его мутация не коммитится.
func PutID(ctx context.Context, tx pgx.Tx, desc TemplateDesc, to string, values Values) (Queued, error) {
	s, ok := sourceFrom(ctx)
	if !ok {
		return Queued{}, ErrSourceUnbound
	}
	return s.put(ctx, tx, desc, to, values)
}

// Put — PutID без id: исходы те же, id записанной строки отбрасывается.
// Его зовут файлы, порождённые notifygen до появления PutID; порождённое
// теперь зовёт PutID. Put снимается, когда ни в одном потребителе corelib
// (kacho, kaname) не остаётся ссылки на feed.Put — проверка: `git grep -n
// 'feed\.Put\b' -- '*.go'` в обоих деревьях пуст.
func Put(ctx context.Context, tx pgx.Tx, desc TemplateDesc, to string, values Values) error {
	_, err := PutID(ctx, tx, desc, to, values)
	return err
}

func (s *Source) put(ctx context.Context, tx pgx.Tx, desc TemplateDesc, to string, values Values) (Queued, error) {
	// 1. Флаг.
	if !s.enabled {
		switch desc.Class {
		case ClassNotice:
			return Queued{}, nil
		case ClassSecurity:
			return Queued{}, ErrDeliveryNotConfigured
		}
		return Queued{}, desc.invalid("класс вне перечня")
	}
	// 2. Описание и состав набора.
	if err := desc.Validate(); err != nil {
		return Queued{}, err
	}
	if err := checkComposition(desc, values); err != nil {
		return Queued{}, err
	}
	// 3. Адресат — по форме описания — и ключи окон.
	recipient, err := recipientOf(desc, to)
	if err != nil {
		return Queued{}, err
	}
	if desc.hasScope(ScopeInitiator) && values.Initiator == "" {
		return Queued{}, desc.invalid("атрибут initiator: лимит на инициатора без инициатора")
	}
	if desc.hasScope(ScopeProject) && !ofFamily(values.Project, familyProject) {
		return Queued{}, desc.invalid("ключ окна project: лимит на проект без id проекта")
	}
	// 4. Атрибуты.
	plain, secret, err := checkAttrs(desc, values)
	if err != nil {
		return Queued{}, err
	}

	id := ids.NewHyphenID(ids.PrefixNotificationHyphen)
	attrsJSON, err := json.Marshal(plain)
	if err != nil {
		return Queued{}, fmt.Errorf("feed: шаблон %s: атрибуты не сериализуются: %w", desc.Name, err)
	}
	var sealed []byte
	if len(secret) > 0 {
		plaintext, err := json.Marshal(secret)
		if err != nil {
			return Queued{}, fmt.Errorf("feed: шаблон %s: секретные атрибуты не сериализуются: %w", desc.Name, err)
		}
		if sealed, err = s.sealer.Seal(s.service, id, desc.Name, plaintext); err != nil {
			return Queued{}, fmt.Errorf("feed: шаблон %s: секрет не запечатан: %w", desc.Name, err)
		}
	}

	// 5–6. Окно, строка, вклад, сигнал — под точкой сохранения.
	sp, err := tx.Begin(ctx)
	if err != nil {
		return Queued{}, fmt.Errorf("feed: точка сохранения: %w", err)
	}
	if err := s.write(ctx, tx, sp, desc, row{
		id: id, recipient: recipient, initiator: values.Initiator, project: values.Project, attrs: attrsJSON, sealed: sealed,
	}); err != nil {
		// Откат к точке сохранения снимает вклад, отметку и замки строк окна
		// этой постановки; транзакция вызывающего остаётся пригодной.
		if rbErr := rollbackSavepoint(ctx, sp); rbErr != nil {
			return Queued{}, errors.Join(err, fmt.Errorf("feed: откат к точке сохранения: %w", rbErr))
		}
		var ex *exhaustedError
		if errors.As(err, &ex) {
			if hookErr := s.suppressedAfterCommit(tx, desc, ex.scope); hookErr != nil {
				return Queued{}, errors.Join(err, hookErr)
			}
		}
		return Queued{}, err
	}
	if err := sp.Commit(ctx); err != nil {
		return Queued{}, fmt.Errorf("feed: освобождение точки сохранения: %w", err)
	}
	return Queued{id: id, queued: true}, nil
}

// rollbackSavepoint откатывает точку сохранения под своим сроком
// savepointRollbackTimeout, отвязанным от отмены вызывающего.
func rollbackSavepoint(ctx context.Context, sp pgx.Tx) error {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), savepointRollbackTimeout)
	defer cancel()
	return sp.Rollback(rctx)
}

type row struct {
	id, recipient, initiator, project string
	attrs, sealed                     []byte
}

// write — шаги 5 и 6 в точке сохранения sp. Сигнал пишется через tx
// вызывающего: писатель журнала требует транзакцию помощника journaltx, а
// оператор всё равно идёт внутри открытой точки сохранения того же соединения.
func (s *Source) write(ctx context.Context, tx, sp pgx.Tx, desc TemplateDesc, r row) error {
	var windows []window
	if len(desc.Limits) > 0 {
		if err := s.markLimitedPut(ctx, sp, desc); err != nil {
			return err
		}
		var err error
		if windows, err = takeWindows(ctx, sp, s.service, desc, windowKeys{recipient: r.recipient, initiator: r.initiator, project: r.project}); err != nil {
			return err
		}
	}
	if _, err := sp.Exec(ctx, fmt.Sprintf(`INSERT INTO %s
		(id, template, schema_rev, class, recipient_address, attrs, secret_attrs, state, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', now() + make_interval(secs => $8))`,
		tablename.Of(s.service, tablename.Outbox)),
		r.id, desc.Name, desc.SchemaRev, string(desc.Class), r.recipient, r.attrs, r.sealed, desc.TTL.Seconds()); err != nil {
		return fmt.Errorf("feed: шаблон %s: вставка строки ленты: %w", desc.Name, err)
	}
	for _, w := range windows {
		if _, err := sp.Exec(ctx, fmt.Sprintf(`INSERT INTO %s
			(notification_id, template, scope, window_seconds, key, window_start)
			VALUES ($1, $2, $3, $4, $5, $6)`, tablename.Of(s.service, tablename.Contrib)),
			r.id, desc.Name, string(w.limit.Scope), w.limit.WindowSeconds, w.key, w.start); err != nil {
			return fmt.Errorf("feed: шаблон %s: вставка вклада: %w", desc.Name, err)
		}
	}
	if err := s.signal.SignalFeed(ctx, tx); err != nil {
		return fmt.Errorf("feed: шаблон %s: сигнал подписки: %w", desc.Name, err)
	}
	return nil
}

// markLimitedPut — первый оператор постановки с лимитами в точке сохранения
// (УК85): непустая отметка — вторая постановка с лимитами, сторож и счётчик
// дефекта; пустая — отметка ставится тем же шагом. Пустоту судит
// coalesce(…, ”) = ”: после первого set_config в сеансе значение вне
// транзакции — пустая строка (М27 (б)).
func (s *Source) markLimitedPut(ctx context.Context, sp pgx.Tx, desc TemplateDesc) error {
	var prior string
	if err := sp.QueryRow(ctx,
		`SELECT coalesce(current_setting($1, true), ''), set_config($1, '1', true)`,
		limitedPutSetting).Scan(&prior, new(string)); err != nil {
		return fmt.Errorf("feed: отметка постановки с лимитами: %w", err)
	}
	if prior != "" {
		s.metrics.observePutDefect(PutDefectSecondLimitedPut)
		return fmt.Errorf("%w: шаблон %s", ErrSecondLimitedPut, desc.Name)
	}
	return nil
}

// suppressedAfterCommit — подавление строки лимитом (NTF-3 Р14, З4, З18):
// notify_suppressed_total растёт хуком после УСПЕШНОГО коммита транзакции
// вызывающего, а не в момент отказа: откат после ErrLimitExhausted подавления
// не утверждает (CX3-07). Хук держит только транзакция помощника journaltx —
// та же, без которой сигнал строки ленты не пишется (ErrNotHelperTx); на иной
// транзакции подавление не считается.
func (s *Source) suppressedAfterCommit(tx pgx.Tx, desc TemplateDesc, scope Scope) error {
	jt, ok := tx.(*journaltx.Tx)
	if !ok {
		return nil
	}
	name := desc.Name
	if err := jt.AfterCommit(func() { s.metrics.observeSuppressed(name, scope) }); err != nil {
		return fmt.Errorf("feed: шаблон %s: счёт подавления: %w", desc.Name, err)
	}
	return nil
}

// Приставки и семейства значений форм адресата и ключа окна project.
// Семейства аккаунта и проекта выпускает служба доступа (её константы
// домена PrefixAccount, PrefixProject); фундамент её не импортирует, поэтому
// написание повторено здесь — как приставки пользователя в каталоге ids.
const (
	subjectUserPrefix  = "user:"
	accountOwnerPrefix = "account:"
	principalTypeUser  = "user"
	familyAccount      = "acc"
	familyProject      = "prj"
)

// ofFamily — id семейства prefix в одной из двух форм записи каталога ids
// (слитной либо дефисной). Пустая строка ни одному семейству не принадлежит.
func ofFamily(id, prefix string) bool {
	return ids.IsValid(id, prefix) || ids.IsValidHyphen(id, prefix)
}

// recipientOf — шаг 3 Put: адресат судится формой ОПИСАНИЯ (NTF-3 Р27), и
// значение не той формы — ErrRecipientInvalid до первого оператора SQL.
// Ответ — значение колонки адресата и ключ окна адресата:
//   - address — address.Normalize, ключ — нормализованный адрес;
//   - subject — user:<id>, id семейства пользователя; форму субъекта судит
//     auth.InitiatorOf, единственное место о ней (З3): иной тип субъекта,
//     иное семейство, пустой id и форма компонента отвергаются. Значение
//     через address.Normalize не идёт и в адресную форму не приводится;
//   - account_owner — account:<id>, id семейства аккаунта;
//   - fanout — адресата нет: только пустое значение.
//
// Текст отказа значения адресата не несёт.
func recipientOf(desc TemplateDesc, to string) (string, error) {
	switch desc.Recipient {
	case RecipientAddress:
		n, err := address.Normalize(to)
		if err != nil {
			return "", fmt.Errorf("%w: шаблон %s: %w", ErrRecipientInvalid, desc.Name, err)
		}
		return recipientKey(n)
	case RecipientSubject:
		if id, ok := strings.CutPrefix(to, subjectUserPrefix); ok {
			in, err := auth.InitiatorOf(operations.Principal{Type: principalTypeUser, ID: id})
			if err == nil && in.String() == to {
				return to, nil
			}
		}
		return "", fmt.Errorf("%w: шаблон %s: форма subject — user:<id пользователя>", ErrRecipientInvalid, desc.Name)
	case RecipientAccountOwner:
		if id, ok := strings.CutPrefix(to, accountOwnerPrefix); ok && ofFamily(id, familyAccount) {
			return to, nil
		}
		return "", fmt.Errorf("%w: шаблон %s: форма account_owner — account:<id аккаунта>", ErrRecipientInvalid, desc.Name)
	case RecipientFanout:
		if to == "" {
			return "", nil
		}
		return "", fmt.Errorf("%w: шаблон %s: у формы fanout адресата нет", ErrRecipientInvalid, desc.Name)
	}
	// Validate (шаг 2) отверг форму вне перечня раньше; ветка — страховка.
	return "", desc.invalid("форма адресата вне перечня")
}

// recipientKey — ключ окна по адресату формы address: только
// address.Normalized. Нулевое значение — ErrRecipientInvalid с причиной
// address.ErrUnset; ключа «пустого адреса» нет (NTF1-B27 (нуль)).
func recipientKey(n address.Normalized) (string, error) {
	v, err := n.Value()
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrRecipientInvalid, err)
	}
	return v, nil
}

// checkComposition — атрибут набора вне описания (в том числе непустой набор
// при пустом описании) — сторож с именем атрибута (NTF1-B28 (Put)).
func checkComposition(desc TemplateDesc, values Values) error {
	declared := make(map[string]bool, len(desc.Attrs))
	for _, a := range desc.Attrs {
		declared[a.Name] = true
	}
	names := make([]string, 0, len(values.Attrs))
	for name := range values.Attrs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !declared[name] {
			return desc.invalid("атрибут %s вне описания", name)
		}
	}
	return nil
}

// checkAttrs — один исчерпывающий обход описания (З7, шаг 4): required без
// значения или с нулём — сторож; optional, переданный нулём, — сторож («не
// задан» выражается только отсутствием ключа); значение вне формы — сторож;
// значение темы — без управляющих символов. Ошибка называет атрибут и
// значения не несёт. Решение «нуль ли это» принимает form.Presence.
func checkAttrs(desc TemplateDesc, values Values) (plain, secret map[string]string, err error) {
	plain = map[string]string{}
	secret = map[string]string{}
	for _, a := range desc.Attrs {
		raw, given := values.Attrs[a.Name]
		if !given {
			if a.Presence == PresenceRequired {
				return nil, nil, desc.invalid("атрибут %s: required без значения", a.Name)
			}
			continue
		}
		v, present, err := form.Presence(a.Kind, raw)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: шаблон %s: атрибут %s: %w", ErrAttrsInvalid, desc.Name, a.Name, err)
		}
		if present == form.Absent {
			return nil, nil, desc.invalid("атрибут %s: нулевое значение (незаданный optional — отсутствие ключа)", a.Name)
		}
		s, err := v.Value()
		if err != nil {
			return nil, nil, fmt.Errorf("%w: шаблон %s: атрибут %s: %w", ErrAttrsInvalid, desc.Name, a.Name, err)
		}
		if a.Subject {
			if _, err := form.ParseHeaderText(s); err != nil {
				return nil, nil, fmt.Errorf("%w: шаблон %s: атрибут %s: %w", ErrAttrsInvalid, desc.Name, a.Name, err)
			}
		}
		if v.Kind() == form.KindSecret {
			secret[a.Name] = s
		} else {
			plain[a.Name] = s
		}
	}
	return plain, secret, nil
}
