// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package operations_test

// Integration-тесты (testcontainers Postgres) терминальной записи, которая
// ВМЕСТЕ с response заменяет metadata операции (MarkDoneWithMetadata).
//
// Зачем метод существует: `MarkDone` пишет ТОЛЬКО response — metadata остаётся
// такой, какой её вписал Create. Для синхронной мутации, чей owning-ресурс
// становится известен лишь ПОСЛЕ записи (id строки приходит из БД, а на
// идемпотентном повторе — id уже существующей строки), это значит, что
// объявленное в контракте поле метаданных не заполняется никогда: op-строку
// приходится создавать ДО мутации (иначе на сбое Create останется
// закоммиченная мутация без pollable-операции), а дописать в неё найденный id
// было нечем.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/PRO-Robotech/corelib/operations"
)

// Терминальная запись заменяет metadata и пишет response одним переходом.
func TestRepo_MarkDoneWithMetadata_ReplacesMetadataAndWritesResponse(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)

	partial, err := anypb.New(wrapperspb.String("partial-metadata"))
	require.NoError(t, err)

	op, err := operations.New("opx", "markdone with metadata", partial)
	require.NoError(t, err)
	require.NoError(t, repo.Create(ctx, op))

	full := mustAnyVal(t, "full-metadata")
	resp := mustAnyVal(t, "final-response")
	require.NoError(t, repo.MarkDoneWithMetadata(ctx, op.ID, full, resp))

	got, err := repo.Get(ctx, op.ID)
	require.NoError(t, err)
	assert.True(t, got.Done, "строка терминальна")
	require.NotNil(t, got.Metadata)
	assert.Equal(t, full.GetValue(), got.Metadata.GetValue(), "metadata заменена")
	require.NotNil(t, got.Response)
	assert.Equal(t, resp.GetValue(), got.Response.GetValue(), "response записан")
	assert.Nil(t, got.Error)
}

// CAS-on-`done`: уже терминальную строку не перезаписываем (симметрия MarkDone).
func TestRepo_MarkDoneWithMetadata_CASOnDone_NoOverwrite(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)

	op, err := operations.New("opx", "markdone with metadata cas", nil)
	require.NoError(t, err)
	require.NoError(t, repo.Create(ctx, op))

	m1, r1 := mustAnyVal(t, "M1"), mustAnyVal(t, "R1")
	require.NoError(t, repo.MarkDoneWithMetadata(ctx, op.ID, m1, r1))

	m2, r2 := mustAnyVal(t, "M2"), mustAnyVal(t, "R2")
	err = repo.MarkDoneWithMetadata(ctx, op.ID, m2, r2)
	require.ErrorIs(t, err, operations.ErrAlreadyDone)

	got, err := repo.Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, m1.GetValue(), got.Metadata.GetValue(), "metadata не перезаписана")
	assert.Equal(t, r1.GetValue(), got.Response.GetValue(), "response не перезаписан")
}

// Несуществующая строка → ErrNotFound (различение вне worker-пути).
func TestRepo_MarkDoneWithMetadata_NonexistentRow_NotFound(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)

	err := repo.MarkDoneWithMetadata(ctx, "opx00000000000000000",
		mustAnyVal(t, "M"), mustAnyVal(t, "R"))
	assert.ErrorIs(t, err, operations.ErrNotFound)
}

// Денормализованные индекс-колонки (resource_id / account_id) выставляются на
// Create и терминальной записью НЕ трогаются: финальная metadata не обязана
// нести их поля, а перевычисление из неё молча обнулило бы уже выставленный
// ключ (операция исчезла бы из своей же выборки). Инвариант проверяем, потому
// что «заменяем metadata» звучит как «перевычисляем всё, что из неё выведено».
func TestRepo_MarkDoneWithMetadata_LeavesDenormalisedIndexKeysIntact(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)

	op, err := operations.New("opx", "denorm index keys", nil)
	require.NoError(t, err)
	op.ResourceID = "res_before"
	require.NoError(t, repo.Create(ctx, op))

	require.NoError(t, repo.MarkDoneWithMetadata(ctx, op.ID,
		mustAnyVal(t, "final-metadata"), mustAnyVal(t, "R")))

	var resourceID *string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT resource_id FROM public.operations WHERE id = $1`, op.ID).Scan(&resourceID))
	require.NotNil(t, resourceID, "индекс-ключ не обнулён терминальной записью")
	assert.Equal(t, "res_before", *resourceID)
}
