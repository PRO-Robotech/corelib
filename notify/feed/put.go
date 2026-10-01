// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package feed — лента почтовых извещений у источника (NTF-1, З6–З12):
// постановка Put в транзакции вызывающего, окно лимита, флаг источника.
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
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/PRO-Robotech/corelib/ids"
	"github.com/PRO-Robotech/corelib/notify/address"
	"github.com/PRO-Robotech/corelib/notify/feed/internal/tablename"
	"github.com/PRO-Robotech/corelib/notify/form"
)

// Values — набор значений постановки. Attrs — значения атрибутов в Go-форме
// form.Presence (string; у timestamp — time.Time или написание ленты); ключ
// незаданного optional в набор не попадает (З5). Initiator — ключ окна
// области initiator; нужен, когда у шаблона есть лимит на инициатора.
type Values struct {
	Initiator string
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

// Put ставит письмо шаблона desc адресату to в транзакции вызывающего tx
// (З7). Источник берётся из контекста (Source.Bind); без него —
// ErrSourceUnbound.
//
// Исходы: nil — строка, вклад в окна и строка журнала подписки записаны (либо
// флаг выключен и класс notice — тогда ничего); иначе один из пяти сторожей
// PutGuards с именем шаблона или атрибута; иначе ошибка хранилища. После
// сторожа транзакция пригодна к коммиту; ErrSecondLimitedPut — дефект
// вызывающего, его мутация не коммитится.
func Put(ctx context.Context, tx pgx.Tx, desc TemplateDesc, to string, values Values) error {
	s, ok := sourceFrom(ctx)
	if !ok {
		return ErrSourceUnbound
	}
	return s.put(ctx, tx, desc, to, values)
}

func (s *Source) put(ctx context.Context, tx pgx.Tx, desc TemplateDesc, to string, values Values) error {
	// 1. Флаг.
	if !s.enabled {
		switch desc.Class {
		case ClassNotice:
			return nil
		case ClassSecurity:
			return ErrDeliveryNotConfigured
		}
		return desc.invalid("класс вне перечня")
	}
	// 2. Описание и состав набора.
	if err := desc.Validate(); err != nil {
		return err
	}
	if err := checkComposition(desc, values); err != nil {
		return err
	}
	// 3. Адресат.
	n, err := address.Normalize(to)
	if err != nil {
		return fmt.Errorf("%w: шаблон %s: %w", ErrRecipientInvalid, desc.Name, err)
	}
	recipient, err := recipientKey(n)
	if err != nil {
		return err
	}
	if desc.hasScope(ScopeInitiator) && values.Initiator == "" {
		return desc.invalid("атрибут initiator: лимит на инициатора без инициатора")
	}
	// 4. Атрибуты.
	plain, secret, err := checkAttrs(desc, values)
	if err != nil {
		return err
	}

	id := ids.NewHyphenID(ids.PrefixNotificationHyphen)
	attrsJSON, err := json.Marshal(plain)
	if err != nil {
		return fmt.Errorf("feed: шаблон %s: атрибуты не сериализуются: %w", desc.Name, err)
	}
	var sealed []byte
	if len(secret) > 0 {
		plaintext, err := json.Marshal(secret)
		if err != nil {
			return fmt.Errorf("feed: шаблон %s: секретные атрибуты не сериализуются: %w", desc.Name, err)
		}
		if sealed, err = s.sealer.Seal(s.service, id, desc.Name, plaintext); err != nil {
			return fmt.Errorf("feed: шаблон %s: секрет не запечатан: %w", desc.Name, err)
		}
	}

	// 5–6. Окно, строка, вклад, сигнал — под точкой сохранения.
	sp, err := tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("feed: точка сохранения: %w", err)
	}
	if err := s.write(ctx, tx, sp, desc, row{
		id: id, recipient: recipient, initiator: values.Initiator, attrs: attrsJSON, sealed: sealed,
	}); err != nil {
		// Откат к точке сохранения снимает вклад, отметку и замки строк окна
		// этой постановки; транзакция вызывающего остаётся пригодной.
		if rbErr := rollbackSavepoint(ctx, sp); rbErr != nil {
			return errors.Join(err, fmt.Errorf("feed: откат к точке сохранения: %w", rbErr))
		}
		return err
	}
	if err := sp.Commit(ctx); err != nil {
		return fmt.Errorf("feed: освобождение точки сохранения: %w", err)
	}
	return nil
}

// rollbackSavepoint откатывает точку сохранения под своим сроком
// savepointRollbackTimeout, отвязанным от отмены вызывающего.
func rollbackSavepoint(ctx context.Context, sp pgx.Tx) error {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), savepointRollbackTimeout)
	defer cancel()
	return sp.Rollback(rctx)
}

type row struct {
	id, recipient, initiator string
	attrs, sealed            []byte
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
		if windows, err = takeWindows(ctx, sp, s.service, desc, r.recipient, r.initiator); err != nil {
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
		s.defects.WithLabelValues(s.module, "second_limited_put").Inc()
		return fmt.Errorf("%w: шаблон %s", ErrSecondLimitedPut, desc.Name)
	}
	return nil
}

// recipientKey — ключ окна по адресату: только address.Normalized. Нулевое
// значение — ErrRecipientInvalid с причиной address.ErrUnset; ключа «пустого
// адреса» нет (NTF1-B27 (нуль)).
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
