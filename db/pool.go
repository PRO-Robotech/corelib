// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool создает pgxpool с server-side таймаутами и проверяет связь с БД (Ping)
// — fail-fast на старте: при недоступной БД возвращается ошибка, а не «ленивый»
// пул, который упадет лишь на первом запросе. Таймаут связи контролируется
// переданным ctx.
//
// Устанавливаемые RuntimeParams (defense-in-depth, независимо от корректности
// app-side ctx):
//   - statement_timeout=30s — потолок исполнения ОДНОГО запроса.
//   - idle_in_transaction_session_timeout=60s — потолок «idle-in-transaction»
//     (транзакция открыта, но не исполняет запрос). Дренер держит claim-tx
//     открытой на время applier-вызова (~5s), reconciler.Sweep — на время
//     одного Resolve (~10s, ResolveTimeout). На каждой итерации Sweep сбрасывает
//     idle-таймер statement'ом: ветки Done/Interrupted — через markDoneCAS/
//     markErrorCAS, а ветки Skip/resolver-error — через явный keep-alive
//     (reconciler.keepClaimAlive: SELECT 1). Поэтому непрерывный idle ограничен
//     одним ResolveTimeout на любой ветке, а не суммой по батчу. 60s
//     даёт запас ~6x над этим потолком и при этом жёстко реапит по-настоящему
//     зависшую tx (минуты), которую app-side ctx проглядел (CGO/DNS-stall,
//     игнорирующий cancel) — иначе она держала бы FOR UPDATE SKIP LOCKED
//     row-locks и блокировала VACUUM (CWE-400).
//
// lock_timeout НЕ выставляется намеренно: ожидание блокировки и так ограничено
// statement_timeout (30s), а отдельный lock_timeout ввёл бы новый класс ошибки
// (SQLSTATE 55P03) на путь contended-CAS во всех сервисах, чьи mapRepoErr его не
// обрабатывают — регрессия без выигрыша сверх statement_timeout.
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "30000"
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "60000"
	// ОТМЕНА ЗАПРОСА ДОВОДИТСЯ ДО POSTGRES, А НЕ ДО СОКЕТА.
	//
	// Умолчание драйвера на отменённом контексте — поставить сокету срок в
	// прошлом. Чтение немедленно ошибается, вызывающий получает управление, и
	// связь закрывается КЛИЕНТСКИ: запрос на сервере при этом никто не отменял,
	// а пул считает связь непригодной и уничтожает её. Наблюдаемая цена — одно
	// подключение за каждый отменённый запрос: под нагрузкой пул не «дорастает
	// до потолка», а непрерывно перебирает связи, и это ложится на базу, которая
	// в замере пропускной способности и оказалась узким местом.
	//
	// Этот обработчик вместо срока на сокете шлёт серверу настоящий CancelRequest:
	// запрос снимается ТАМ, где исполняется, связь остаётся пригодной и
	// возвращается в пул. Срок на сокете остаётся запасным путём — на случай,
	// когда отмена до сервера не доехала.
	//
	// ЦЕНА НАЗВАНА, а не умолчана, и она двойная:
	//   * сам CancelRequest идёт отдельным коротким подключением — так устроен
	//     протокол;
	//   * снятие наблюдения ждёт ~100 мс (сон внутри драйвера: без него отмена
	//     успевает догнать СЛЕДУЮЩИЙ запрос той же связи).
	// То есть каждая отмена стоит около десятой доли секунды времени горутины.
	// Это дороже прежнего по времени клиента и ДЕШЕВЛЕ по базе: уничтоженная
	// связь оплачивается новым рукопожатием и новым обслуживающим процессом на
	// стороне, которая в замере пропускной способности и оказалась узкой.
	//
	// DeadlineDelay — запасной путь, когда отмена до сервера не доехала. Полсекунды
	// на порядок больше здорового круга внутри кластера и при этом не превращают
	// отмену в зависание: наблюдалось прямо — с пятью секундами дренер перестал
	// укладываться в свои три секунды на выход, и это увидела проба, а не стенд.
	cfg.ConnConfig.BuildContextWatcherHandler = func(c *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{
			Conn:               c,
			CancelRequestDelay: 0,
			DeadlineDelay:      500 * time.Millisecond,
		}
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping after pool creation: %w", err)
	}
	return pool, nil
}
