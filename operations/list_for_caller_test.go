// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package operations_test

// Контракт ListForCaller — единственной точки, через которую tenant-facing
// список операций попадает в сервисы.
//
// Проверяется наблюдаемое: какие строки вернулись. Фейк ниже реализует ОБА
// пути — несуженный List (отдаёт всё) и суженный ListOwned (предикат владения),
// поэтому тест краснеет ровно тогда, когда вызывается несуженный. Утверждения
// «позвана та функция» здесь нет и быть не должно: оно осталось бы зелёным при
// суженном вызове с ключом, который матчит всех.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/PRO-Robotech/corelib/operations"
)

// listOnlyRepo — Repo БЕЗ ownership-scoped апгрейда: несуженный путь есть,
// суженного нет. Обязан получить отказ, а не тихую несуженную выдачу.
type listOnlyRepo struct {
	rows []operations.Operation
}

func (r *listOnlyRepo) Create(context.Context, operations.Operation) error { return nil }
func (r *listOnlyRepo) CreateWithPrincipal(context.Context, operations.Operation, operations.Principal) error {
	return nil
}

func (r *listOnlyRepo) Get(context.Context, string) (*operations.Operation, error) {
	return nil, operations.ErrNotFound
}
func (r *listOnlyRepo) MarkDone(context.Context, string, *anypb.Any) error   { return nil }
func (r *listOnlyRepo) MarkError(context.Context, string, *spb.Status) error { return nil }
func (r *listOnlyRepo) Cancel(context.Context, string) error                 { return nil }

func (r *listOnlyRepo) List(_ context.Context, _ operations.ListFilter) ([]operations.Operation, string, error) {
	return append([]operations.Operation(nil), r.rows...), "", nil
}

// bothPathsRepo добавляет к несуженному пути суженный.
type bothPathsRepo struct {
	listOnlyRepo
}

func (r *bothPathsRepo) GetOwned(context.Context, string, operations.Owner) (*operations.Operation, error) {
	return nil, operations.ErrNotFound
}

func (r *bothPathsRepo) CancelOwned(context.Context, string, operations.Owner) (*operations.Operation, error) {
	return nil, operations.ErrNotFound
}

func (r *bothPathsRepo) ListOwned(_ context.Context, _ operations.ListFilter, owner operations.Owner) ([]operations.Operation, string, error) {
	var out []operations.Operation
	for _, op := range r.rows {
		if op.Principal.Type == owner.PrincipalType && op.Principal.ID == owner.PrincipalID {
			out = append(out, op)
		}
	}
	return out, "", nil
}

var (
	callerAlice = operations.Principal{Type: "user", ID: "usr-alice", DisplayName: "alice@kacho.local"}
	callerBob   = operations.Principal{Type: "user", ID: "usr-bob", DisplayName: "bob@kacho.local"}
)

func twoOwnersRepo() *bothPathsRepo {
	return &bothPathsRepo{listOnlyRepo{rows: []operations.Operation{
		{ID: "op-alice", ResourceID: "net-1", Principal: callerAlice},
		{ID: "op-bob", ResourceID: "net-1", Principal: callerBob},
	}}}
}

func TestListForCaller_ReturnsOwnRowsOnly(t *testing.T) {
	repo := twoOwnersRepo()
	ctx := operations.WithPrincipal(context.Background(), callerAlice)

	got, next, err := operations.ListForCaller(ctx, repo, operations.ListFilter{ResourceID: "net-1"})
	require.NoError(t, err)
	assert.Empty(t, next)
	require.Len(t, got, 1, "у вызывающего одна своя операция; чужая не показывается")
	assert.Equal(t, "op-alice", got[0].ID)
	assert.NotContains(t, got[0].Principal.DisplayName, "bob",
		"личность на строке — личность самого вызывающего, а не чужая")
}

func TestListForCaller_UnidentifiedCallerGetsEmptyPage(t *testing.T) {
	repo := twoOwnersRepo()

	got, next, err := operations.ListForCaller(context.Background(), repo,
		operations.ListFilter{ResourceID: "net-1"})
	require.NoError(t, err)
	assert.Empty(t, next)
	assert.Empty(t, got, "ключа владения нет → своих операций нет; несуженная выдача запрещена")
}

// Именованная анонимность («неизвестно кто») — ключ, который совпадает сам с
// собой у любых двух безымянных запросов. Владельцем он быть не может.
func TestListForCaller_NamedAnonymityIsNotAnOwner(t *testing.T) {
	repo := &bothPathsRepo{listOnlyRepo{rows: []operations.Operation{
		{ID: "op-anon", ResourceID: "net-1", Principal: operations.Principal{
			Type: "system", ID: operations.AnonymousPrincipalID}},
	}}}
	ctx := operations.WithPrincipal(context.Background(), operations.Principal{
		Type: "system", ID: operations.AnonymousPrincipalID})

	got, _, err := operations.ListForCaller(ctx, repo, operations.ListFilter{ResourceID: "net-1"})
	require.NoError(t, err)
	assert.Empty(t, got, "безымянный ключ не владеет ничем, иначе один безымянный читает за другого")
}

// Репозиторий без суженного пути — отказ, а не откат на несуженный.
func TestListForCaller_RepoWithoutOwnedPathIsRefused(t *testing.T) {
	repo := &listOnlyRepo{rows: []operations.Operation{
		{ID: "op-alice", ResourceID: "net-1", Principal: callerAlice},
		{ID: "op-bob", ResourceID: "net-1", Principal: callerBob},
	}}
	ctx := operations.WithPrincipal(context.Background(), callerAlice)

	got, _, err := operations.ListForCaller(ctx, repo, operations.ListFilter{ResourceID: "net-1"})
	require.Error(t, err, "непровязанный ownership-путь обязан отказать")
	assert.Empty(t, got, "ни одной строки при отказе")
	assert.Equal(t, codes.Internal, grpcstatus.Code(err))
	assert.Equal(t, "list operations failed", grpcstatus.Convert(err).Message(),
		"текст отказа фиксированный: тип репозитория и прочие детали реализации наружу не идут")
}

// Формат страницы проверяется ДО того, как выдача схлопывается в пустую: иначе
// мусорный курсор у неопознанного вызывающего вернул бы пустую страницу вместо
// отказа по формату (api-conventions: format-validate → authz → repo).
func TestListForCaller_PageTokenValidatedBeforeEmptyShortCircuit(t *testing.T) {
	repo := twoOwnersRepo()

	_, _, err := operations.ListForCaller(context.Background(), repo,
		operations.ListFilter{ResourceID: "net-1", PageToken: "!!!not-base64!!!"})
	require.Error(t, err, "мусорный page_token обязан отвергаться и без ключа владения")
	assert.Equal(t, codes.InvalidArgument, grpcstatus.Code(err))
}

func TestListForCaller_PageSizeValidatedBeforeEmptyShortCircuit(t *testing.T) {
	repo := twoOwnersRepo()

	_, _, err := operations.ListForCaller(context.Background(), repo,
		operations.ListFilter{ResourceID: "net-1", PageSize: 100500})
	require.Error(t, err, "page_size вне диапазона обязан отвергаться и без ключа владения")
	assert.Equal(t, codes.InvalidArgument, grpcstatus.Code(err))
}
