// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package feed

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/notify/form"
)

// callerTx — транзакция вызывающего: Begin отдаёт точку сохранения sp.
// Прочие методы pgx.Tx не реализованы: путь, дошедший до них, падает
// разыменованием nil и называет себя.
type callerTx struct {
	pgx.Tx
	sp *savepointTx
}

func (c *callerTx) Begin(context.Context) (pgx.Tx, error) { return c.sp, nil }

// savepointTx — точка сохранения, чей первый оператор отказывает; Rollback
// записывает контекст, с которым его позвали.
type savepointTx struct {
	pgx.Tx
	rollbackCtx context.Context
	rollbackErr error // ctx.Err() в момент вызова: после возврата срок снят cancel
	rolledBack  bool
}

var errStatement = errors.New("оператор отказал")

func (s *savepointTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errStatement
}

func (s *savepointTx) Rollback(ctx context.Context) error {
	s.rollbackCtx = ctx
	s.rollbackErr = ctx.Err()
	s.rolledBack = true
	return nil
}

// recordingSealer запоминает, что Put передал в AAD.
type recordingSealer struct{ service, id, template string }

func (r *recordingSealer) Seal(service, id, template string, plaintext []byte) ([]byte, error) {
	r.service, r.id, r.template = service, id, template
	return []byte("sealed"), nil
}

func unitSource(t *testing.T, sealer Sealer) *Source {
	t.Helper()
	en, err := ParseEnabled("KACHO_PROBE_NOTIFICATIONS_ENABLED",
		func(string) (string, bool) { return "true", true })
	require.NoError(t, err)
	src, err := NewSource(Config{
		Module: "probe", Service: "probe", Enabled: en,
		Signal: noSignal{}, Sealer: sealer, Metrics: prometheus.NewRegistry(),
	})
	require.NoError(t, err)
	return src
}

type noSignal struct{}

func (noSignal) SignalFeed(context.Context, pgx.Tx) error { return nil }

// Шаблон без лимитов: первый оператор под точкой сохранения — вставка строки.
var sealedDesc = TemplateDesc{
	Name: "probe", Class: ClassSecurity, SchemaRev: 1, TTL: time.Hour,
	Attrs: []AttrDesc{{Name: "code", Kind: form.KindSecret, Presence: PresenceRequired}},
}

// arch-per-call-deadline: откат к точке сохранения после отказа оператора
// отвязан от отмены вызывающего (транзакция вызывающего обязана остаться
// пригодной), но ограничен своим сроком savepointRollbackTimeout — и тогда,
// когда срок вызывающего уже истёк. Близнец: откат без отказа не зовётся.
func TestSavepointRollbackRunsUnderItsOwnDeadline(t *testing.T) {
	src := unitSource(t, &recordingSealer{})
	callerCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	cancel()
	ctx := src.Bind(callerCtx)

	sp := &savepointTx{}
	before := time.Now()
	err := Put(ctx, &callerTx{sp: sp}, sealedDesc, "user@example.invalid",
		Values{Attrs: map[string]any{"code": "123456"}})
	require.ErrorIs(t, err, errStatement)
	require.True(t, sp.rolledBack, "отказ оператора обязан откатить точку сохранения")

	deadline, ok := sp.rollbackCtx.Deadline()
	require.True(t, ok, "откат к точке сохранения идёт без срока")
	require.NoError(t, sp.rollbackErr, "откат унаследовал отмену вызывающего")
	require.False(t, deadline.Before(before.Add(savepointRollbackTimeout)),
		"срок отката короче savepointRollbackTimeout")
	require.False(t, deadline.After(time.Now().Add(savepointRollbackTimeout)),
		"срок отката длиннее savepointRollbackTimeout")
	require.Positive(t, savepointRollbackTimeout)
}

// Шов AAD с полосой C5: Put передаёт запечатыванию префикс службы
// Config.Service, и порт называет первый параметр так же — service, а не
// table: C5 собирает AAD открытия из того же значения и видит одну форму.
func TestSealerReceivesTheServicePrefixUnderItsOwnName(t *testing.T) {
	rec := &recordingSealer{}
	src := unitSource(t, rec)
	err := Put(src.Bind(context.Background()), &callerTx{sp: &savepointTx{}}, sealedDesc,
		"user@example.invalid", Values{Attrs: map[string]any{"code": "123456"}})
	require.ErrorIs(t, err, errStatement)
	require.Equal(t, "probe", rec.service)
	require.Equal(t, "probe", rec.template)
	require.NotEmpty(t, rec.id)

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "put_source.go", nil, 0)
	require.NoError(t, err)
	var params []string
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "Sealer" {
			return true
		}
		for _, m := range ts.Type.(*ast.InterfaceType).Methods.List {
			if m.Names[0].Name != "Seal" {
				continue
			}
			for _, p := range m.Type.(*ast.FuncType).Params.List {
				for _, name := range p.Names {
					params = append(params, name.Name)
				}
			}
		}
		return false
	})
	require.NotEmpty(t, params, "в put_source.go нет метода Sealer.Seal")
	require.Equal(t, "service", params[0], "первый параметр Sealer.Seal")
}
