// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package operations

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/types/known/anypb"
)

// TxWriter — запись операции в транзакции ВЫЗЫВАЮЩЕГО: строка операции ложится
// в ту же транзакцию, что и мутация вызывающего, и откатывается вместе с ней.
//
// Четыре функции, по одной на запись:
//   - CreatePendingTx (Ф1) — создание незавершённой операции (done = false);
//   - CreateDoneTx (Ф2) — создание операции, рождённой завершённой: done = true
//     и столбцы ответа в одной вставке;
//   - MarkDoneTx (Ф3) — условная терминальная запись успеха
//     (WHERE id = $1 AND done = false), та же, что у пулового MarkDone;
//   - MarkErrorTx (Ф4) — условная терминальная запись ошибки с тем же условием,
//     та же, что у пулового MarkError.
//
// Каждая пишет ТОЛЬКО строку операции своего хранилища. Всё, что должно лечь в
// ту же транзакцию (строка ленты, строка `operation-failed`), ставит вызывающий
// своей записью. У Ф3 и Ф4 ноль строк — ErrAlreadyDone (строка уже терминальна,
// в том числе отменена CancelOwned) либо ErrNotFound (строки нет), и вызывающий
// откатывает транзакцию целиком. Условие `done = false` у Ф3, Ф4 и CancelOwned
// общее и живёт в одном пакете; на READ COMMITTED конкурирующие записи
// сериализуются блокировкой строки, и вторая находит done = true.
//
// Параметр — pgx.Tx: пул его не удовлетворяет, и запись вне транзакции
// вызывающего не компилируется. nil вместо транзакции — ErrNilTx до первого
// запроса; ветви «транзакции нет — открою свою» нет. Своя транзакция — это
// пуловые MarkDone/MarkError.
//
// TxWriter НЕ входит в Repo и FullRepo: надстройка, встраивающая FullRepo, его не
// удовлетворяет, и подать её туда, где ждут TxWriter, — отказ сборки, а не
// запись мимо надстройки.
type TxWriter interface {
	// CreatePendingTx (Ф1) вставляет операцию с done = false и принципалом p.
	// Принципал, который IsAnonymous, — ErrEmptyPrincipal без записи.
	CreatePendingTx(ctx context.Context, tx pgx.Tx, op Operation, p Principal) error
	// CreateDoneTx (Ф2) вставляет операцию с done = true и ответом response.
	// Принципал, который IsAnonymous, — ErrEmptyPrincipal без записи.
	CreateDoneTx(ctx context.Context, tx pgx.Tx, op Operation, p Principal, response *anypb.Any) error
	// MarkDoneTx (Ф3) — условная терминальная запись успеха.
	MarkDoneTx(ctx context.Context, tx pgx.Tx, id string, response *anypb.Any) error
	// MarkErrorTx (Ф4) — условная терминальная запись ошибки.
	MarkErrorTx(ctx context.Context, tx pgx.Tx, id string, st *status.Status) error
}

// TxRepo — то, что возвращает NewRepo: FullRepo плюс запись в транзакции
// вызывающего. Включает FullRepo, поэтому значение присваивается в любое место,
// ожидающее Repo или FullRepo, как прежде.
type TxRepo interface {
	FullRepo
	TxWriter
}

var _ TxRepo = (*pgRepo)(nil)

// ErrNilTx — функции записи в транзакции вызывающего передан nil вместо
// транзакции. Отказ до первого запроса.
var ErrNilTx = errors.New("operations: nil transaction")

// ErrEmptyPrincipal — функции создания передан принципал, который не называет
// никого (Principal.IsAnonymous). Запасной SystemPrincipal не подставляется:
// такая операция не принадлежала бы никому, и GetOwned/CancelOwned отсекали бы
// её для любого вызывающего.
var ErrEmptyPrincipal = errors.New("operations: principal names no one")

// CreatePendingTx — Ф1, см. TxWriter.
func (r *pgRepo) CreatePendingTx(ctx context.Context, tx pgx.Tx, op Operation, p Principal) error {
	if tx == nil {
		return ErrNilTx
	}
	if p.IsAnonymous() {
		return ErrEmptyPrincipal
	}
	if err := insertOperationTx(ctx, tx, r.tableName(), op, p, false, nil); err != nil {
		return fmt.Errorf("repo.CreatePendingTx: %w", err)
	}
	return nil
}

// CreateDoneTx — Ф2, см. TxWriter.
func (r *pgRepo) CreateDoneTx(ctx context.Context, tx pgx.Tx, op Operation, p Principal, response *anypb.Any) error {
	if tx == nil {
		return ErrNilTx
	}
	if p.IsAnonymous() {
		return ErrEmptyPrincipal
	}
	if err := insertOperationTx(ctx, tx, r.tableName(), op, p, true, response); err != nil {
		return fmt.Errorf("repo.CreateDoneTx: %w", err)
	}
	return nil
}

// MarkDoneTx — Ф3, см. TxWriter. Ровно один вызов markDoneCAS: условие и
// классификация нуля строк — те же, что у пулового MarkDone.
func (r *pgRepo) MarkDoneTx(ctx context.Context, tx pgx.Tx, id string, response *anypb.Any) error {
	if tx == nil {
		return ErrNilTx
	}
	return markDoneCAS(ctx, tx, r.tableName(), id, response)
}

// MarkErrorTx — Ф4, см. TxWriter. Ровно один вызов markErrorCAS.
func (r *pgRepo) MarkErrorTx(ctx context.Context, tx pgx.Tx, id string, st *status.Status) error {
	if tx == nil {
		return ErrNilTx
	}
	return markErrorCAS(ctx, tx, r.tableName(), id, st)
}

// execer — общий для *pgxpool.Pool и pgx.Tx интерфейс Exec: построитель вставки
// работает и в транзакции вызывающего (Ф1, Ф2), и на пуле (CreateWithPrincipal).
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// insertOperationTx — единственный построитель вставки строки операции.
//
// Принципал он пишет как есть и запасного не содержит: Ф1 и Ф2 отказывают
// анонимному до вызова, а CreateWithPrincipal подставляет свой запасной путь
// сам, до вызова. done = true пишет столбцы ответа в той же вставке — операция
// «рождённая завершённой»; при done = false ответ не пишется.
func insertOperationTx(ctx context.Context, q execer, table string, op Operation, p Principal, done bool, response *anypb.Any) error {
	metaType, metaData, err := marshalAny(op.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	var respType *string
	var respData []byte
	if done {
		respType, respData, err = marshalAny(response)
		if err != nil {
			return fmt.Errorf("marshal response: %w", err)
		}
	}

	// Денормализованный индекс resource_id: предпочитаем ЯВНО заданный
	// op.ResourceID (use-case знает owning-ресурс точно); только если он пуст —
	// reflection-fallback на первое `*_id`-поле метаданных.
	resourceID := resolveResourceID(op)
	// account_id — по ТОЧНОМУ имени поля метаданных; нет поля → SQL NULL.
	accountID := extractAccountID(op.Metadata)

	createdBy := op.CreatedBy
	if createdBy == "" {
		createdBy = "anonymous"
	}

	sql := fmt.Sprintf(`
		INSERT INTO %s
		  (id, description, created_at, created_by, modified_at, done,
		   metadata_type, metadata_data, resource_id, account_id,
		   principal_type, principal_id, principal_display_name,
		   response_type, response_data)
		VALUES
		  ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		table,
	)
	if _, err := q.Exec(ctx, sql,
		op.ID,
		op.Description,
		op.CreatedAt,
		createdBy,
		op.ModifiedAt,
		done,
		metaType,
		metaData,
		nullableString(resourceID),
		nullableString(accountID),
		p.Type,
		p.ID,
		p.DisplayName,
		respType,
		respData,
	); err != nil {
		return err
	}
	return nil
}
