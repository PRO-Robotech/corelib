// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package journaltx — помощник транзакции, пишущей журнал подписки или таблицу
// с журналом (NTF-3, З4).
//
// # Что он делает
//
// [Begin] — единственная точка открытия такой транзакции. Первым оператором он
// выставляет две настройки ЛОКАЛЬНО к транзакции (`set_config(…, true)`):
//
//   - [SettingInitiator] — инициатор изменения. Его читает умолчание колонки
//     `initiator` журнала (`NULLIF(current_setting(…, true), ”)`); оператор
//     вставки колонку не называет. Источник у помощника один — принципал
//     контекста, прочитанный `operations.PrincipalFromContextOK` и переведённый
//     `auth.InitiatorOf`;
//   - [SettingFeedEnabled] — флаг ленты модуля, прочитанный корнем модуля один
//     раз при старте и пришедший только в [Options].
//
// Локальность — несущее свойство: соединение возвращается в пул, и настройка,
// выставленная сессионно, стала бы инициатором ЧУЖОЙ следующей транзакции.
//
// # Отказы до первого оператора
//
// Нулевые [Options] ([ErrOptionsUnset]) и принципал без формы инициатора
// (`auth.ErrNoInitiator`) отвергаются до обращения к базе: транзакция не
// открывается, подстановки нет. Это разные отказы, и `errors.Is` их различает.
//
// # Границы импорта
//
// Пакет не импортирует `notify/*`; `operations` его не импортирует
// (терминальная запись ошибки операции зовёт его из `operations/opsnotify`).
package journaltx

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/PRO-Robotech/corelib/auth"
	"github.com/PRO-Robotech/corelib/operations"
)

// Имена настроек транзакции. Их читают умолчание колонки инициатора в
// миграциях модулей и функция базы `resource-event`; генератор берёт имена
// отсюда же.
const (
	SettingInitiator   = "kacho_journal.initiator"
	SettingFeedEnabled = "kacho_feed.enabled"
)

var (
	// ErrOptionsUnset — [Options] не построены [NewOptions]. Флаг `false` из
	// незаданного значения неотличим от выключенного модуля, поэтому
	// незаданное не принимается. Не оборачивает `auth.ErrNoInitiator`.
	ErrOptionsUnset = errors.New("journaltx: Options are not built by NewOptions")

	// ErrComponentOverPrincipal — [AsComponent] позван на контексте, чей
	// принципал даёт иного инициатора либо инициатора не даёт вовсе. Выбора
	// одного из двух нет: у вызывающего это ошибка программы.
	ErrComponentOverPrincipal = errors.New("journaltx: component identity over a different principal")

	// ErrComponentUnnamed — [AsComponent] позван с пустой службой или ролью.
	// Отказ стоит до `auth.SystemPrincipalFor`, чья запасная ветка на пустом
	// входе отдала бы `{system, bootstrap}`.
	ErrComponentUnnamed = errors.New("journaltx: component service or role is empty")
)

// Options — то, что корень модуля передаёт помощнику: флаг ленты модуля.
//
// Строит только [NewOptions]. У нулевого значения признака «построено» нет, и
// [Begin] и конструкторы держателей (писатели журнала модуля,
// `opsnotify.New`) его отвергают ([Options.Validate]). Функциональной опции для
// Options нет: её отсутствие было бы нулевым значением.
type Options struct {
	feedEnabled bool
	built       bool
}

// NewOptions строит Options из единственного чтения ручки флага модуля.
func NewOptions(feedEnabled bool) Options {
	return Options{feedEnabled: feedEnabled, built: true}
}

// FeedEnabled — значение флага ленты модуля.
func (o Options) FeedEnabled() bool { return o.feedEnabled }

// Validate отвергает нулевые Options [ErrOptionsUnset]. Её зовут конструкторы
// держателей Options при сборке корня: отказ старта с именем модуля — первая
// точка обнаружения, отказ [Begin] — страховка.
func (o Options) Validate() error {
	if !o.built {
		return ErrOptionsUnset
	}
	return nil
}

// TxStarter — источник транзакций: `*pgxpool.Pool`, `*pgx.Conn`.
type TxStarter interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// Tx — транзакция помощника. Встраивает `pgx.Tx` и потому годна всюду, где
// ждут `pgx.Tx`; [Tx.Commit] переопределён ради хуков после коммита.
//
// Значение не предназначено для параллельного использования — как и `pgx.Tx`.
type Tx struct {
	pgx.Tx
	initiator auth.Initiator
	hooks     []func()
	finished  bool
}

// Initiator — инициатор, выставленный этой транзакции: то же значение, что ушло
// в [SettingInitiator].
func (t *Tx) Initiator() auth.Initiator { return t.initiator }

// AfterCommit регистрирует хук, исполняемый только после УСПЕШНОГО [Tx.Commit],
// в порядке регистрации. Откат и неудавшийся коммит хуков не исполняют: счётчик,
// растущий хуком, не утверждает того, чего не было (NTF-3, CX3-07).
//
// Регистрация на завершённой транзакции — ошибка программы: хук не исполнился
// бы никогда, и это было бы молчанием, поэтому она паникует.
func (t *Tx) AfterCommit(f func()) {
	if t.finished {
		panic("journaltx: AfterCommit on a finished transaction")
	}
	t.hooks = append(t.hooks, f)
}

// Commit фиксирует транзакцию и при успехе исполняет хуки [Tx.AfterCommit].
func (t *Tx) Commit(ctx context.Context) error {
	hooks := t.hooks
	t.hooks = nil
	t.finished = true
	if err := t.Tx.Commit(ctx); err != nil {
		return err
	}
	for _, f := range hooks {
		f()
	}
	return nil
}

// Rollback откатывает транзакцию; хуки снимаются неисполненными.
func (t *Tx) Rollback(ctx context.Context) error {
	t.hooks = nil
	t.finished = true
	return t.Tx.Rollback(ctx)
}

// Begin открывает транзакцию, пишущую журнал. Порядок: Options → принципал →
// открытие → первый оператор `set_config` обеих настроек локально к
// транзакции. Отказ до открытия транзакции к базе не обращается.
func Begin(ctx context.Context, src TxStarter, opts Options) (*Tx, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	p, ok := operations.PrincipalFromContextOK(ctx)
	if !ok {
		return nil, fmt.Errorf("journaltx: no principal in context: %w", auth.ErrNoInitiator)
	}
	initiator, err := auth.InitiatorOf(p)
	if err != nil {
		return nil, fmt.Errorf("journaltx: %w", err)
	}

	tx, err := src.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("journaltx: begin: %w", err)
	}
	feed := "false"
	if opts.feedEnabled {
		feed = "true"
	}
	if _, err := tx.Exec(ctx,
		`SELECT set_config($1, $2, true), set_config($3, $4, true)`,
		SettingInitiator, initiator.String(), SettingFeedEnabled, feed); err != nil {
		_ = tx.Rollback(ctx)
		return nil, fmt.Errorf("journaltx: set transaction settings: %w", err)
	}
	return &Tx{Tx: tx, initiator: initiator}, nil
}

// AsComponent кладёт в контекст принципал компонента `auth.SystemPrincipalFor(
// service, role)` — личность фонового пути, пишущего журнал (NTF-3, З4).
//
// Исходов на входе три, и у каждого своё решение:
//
//   - принципала в контексте нет — ставится принципал компонента; контекст,
//     с которого принципал снят `operations.WithoutPrincipal`, — отказ
//     [ErrComponentOverPrincipal] (снятие сильнее установки);
//   - принципал есть и даёт того же инициатора — повтор на проходе, отдаётся
//     тот же контекст;
//   - принципал есть и даёт иного инициатора либо не даёт никакого
//     (`{system, bootstrap}`) — [ErrComponentOverPrincipal], контекст не
//     отдаётся.
//
// Пустая служба или роль — [ErrComponentUnnamed]; пара, чей инициатор не
// выразим (имя не DNS-метка), — `auth.ErrNoInitiator`.
func AsComponent(ctx context.Context, service, role string) (context.Context, error) {
	if service == "" || role == "" {
		return nil, ErrComponentUnnamed
	}
	component := auth.SystemPrincipalFor(service, role)
	want, err := auth.InitiatorOf(component)
	if err != nil {
		return nil, fmt.Errorf("journaltx: component (%s, %s): %w", service, role, err)
	}
	p, ok := operations.PrincipalFromContextOK(ctx)
	if !ok {
		out := operations.WithPrincipal(ctx, component)
		// Контекст, с которого принципал СНЯТ (`operations.WithoutPrincipal` —
		// недоверенный пересланный носитель), снятие держит сильнее любой
		// последующей установки: принципал компонента в нём не читается, и
		// транзакция под ним не открылась бы. Отдать такой контекст значило бы
		// отложить отказ до Begin; снятие означает, что носитель БЫЛ, поэтому
		// исход — отказ «поверх принципала», а не тихая установка.
		if _, set := operations.PrincipalFromContextOK(out); !set {
			return nil, fmt.Errorf("%w: principal was scrubbed from the context", ErrComponentOverPrincipal)
		}
		return out, nil
	}
	got, err := auth.InitiatorOf(p)
	if err != nil || got != want {
		return nil, fmt.Errorf("%w: component %s", ErrComponentOverPrincipal, want)
	}
	return ctx, nil
}
