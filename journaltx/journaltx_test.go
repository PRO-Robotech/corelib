// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package journaltx_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/PRO-Robotech/corelib/auth"
	"github.com/PRO-Robotech/corelib/ids"
	"github.com/PRO-Robotech/corelib/journaltx"
	"github.com/PRO-Robotech/corelib/operations"
)

// countingStarter — источник транзакций, который считает попытки открыть
// транзакцию и ни одной не открывает. Им доказывается «отказ ДО первого
// оператора»: у отвергнутого Begin обращений к нему ноль.
type countingStarter struct{ calls int }

func (c *countingStarter) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	c.calls++
	return nil, errors.New("countingStarter: транзакций не открывает")
}

func userCtx() context.Context {
	return operations.WithPrincipal(context.Background(),
		operations.Principal{Type: "user", ID: ids.NewID(ids.PrefixUser)})
}

// TestBegin_ZeroOptionsAreRefusedBeforeTheFirstStatement — нулевые Options
// отвергнуты ErrOptionsUnset до обращения к базе, и этот отказ отличим от
// отказа нормализации субъекта (CX3L-02, CX3M-02 (в)).
func TestBegin_ZeroOptionsAreRefusedBeforeTheFirstStatement(t *testing.T) {
	src := &countingStarter{}
	tx, err := journaltx.Begin(userCtx(), src, journaltx.Options{})
	if err == nil {
		t.Fatal("Begin с нулевыми Options открыл транзакцию")
	}
	if tx != nil {
		t.Error("при отказе отдана транзакция")
	}
	if !errors.Is(err, journaltx.ErrOptionsUnset) {
		t.Errorf("отказ %v не несёт ErrOptionsUnset", err)
	}
	if errors.Is(err, auth.ErrNoInitiator) {
		t.Errorf("отказ %v неотличим от отказа нормализации субъекта", err)
	}
	if src.calls != 0 {
		t.Errorf("обращений к базе %d, ожидалось 0", src.calls)
	}
}

// TestBegin_WithoutPrincipalIsRefusedBeforeTheFirstStatement — у Begin один
// источник инициатора, принципал контекста; нет его или он не нормализуется —
// отказ до обращения к базе, подстановки нет (CX3C-02 (б), CX3D-01 (а)).
func TestBegin_WithoutPrincipalIsRefusedBeforeTheFirstStatement(t *testing.T) {
	cases := map[string]context.Context{
		"контекст без принципала": context.Background(),
		"принципал снят":          operations.WithoutPrincipal(userCtx()),
		"{system, bootstrap}":     operations.WithPrincipal(context.Background(), operations.SystemPrincipal()),
		"анонимный край": operations.WithPrincipal(context.Background(),
			operations.Principal{Type: "system", ID: operations.AnonymousPrincipalID}),
	}
	for name, ctx := range cases {
		src := &countingStarter{}
		tx, err := journaltx.Begin(ctx, src, journaltx.NewOptions(true))
		if err == nil {
			t.Errorf("%s: Begin открыл транзакцию", name)
			continue
		}
		if tx != nil {
			t.Errorf("%s: при отказе отдана транзакция", name)
		}
		if !errors.Is(err, auth.ErrNoInitiator) {
			t.Errorf("%s: отказ %v не несёт auth.ErrNoInitiator", name, err)
		}
		if errors.Is(err, journaltx.ErrOptionsUnset) {
			t.Errorf("%s: отказ %v неотличим от нулевых Options", name, err)
		}
		if src.calls != 0 {
			t.Errorf("%s: обращений к базе %d, ожидалось 0", name, src.calls)
		}
	}
}

// TestOptions — построенное значение отличимо от нулевого при обоих значениях
// флага: `false` из незаданного значения не принимается за выключенный модуль.
func TestOptions(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		o := journaltx.NewOptions(enabled)
		if err := o.Validate(); err != nil {
			t.Errorf("NewOptions(%v).Validate() = %v", enabled, err)
		}
		if o.FeedEnabled() != enabled {
			t.Errorf("NewOptions(%v).FeedEnabled() = %v", enabled, o.FeedEnabled())
		}
	}
	if err := (journaltx.Options{}).Validate(); !errors.Is(err, journaltx.ErrOptionsUnset) {
		t.Errorf("Options{}.Validate() = %v, ожидался ErrOptionsUnset", err)
	}
}

// TestAsComponent — три исхода на входе и отказ на неназванной паре (З4 (2), (3)).
func TestAsComponent(t *testing.T) {
	t.Run("пустой контекст — принципал пары", func(t *testing.T) {
		ctx, err := journaltx.AsComponent(context.Background(), "nlb", "free-ip-runner")
		if err != nil {
			t.Fatalf("отказ: %v", err)
		}
		p, ok := operations.PrincipalFromContextOK(ctx)
		if !ok {
			t.Fatal("принципал в контексте не установлен")
		}
		if want := auth.SystemPrincipalFor("nlb", "free-ip-runner"); p != want {
			t.Errorf("принципал %+v, ожидался %+v", p, want)
		}
	})

	t.Run("снятый принципал — отказ, а не нечитаемая установка", func(t *testing.T) {
		out, err := journaltx.AsComponent(operations.WithoutPrincipal(userCtx()), "storage", "reconciler")
		if !errors.Is(err, journaltx.ErrComponentOverPrincipal) {
			t.Fatalf("отказ %v, ожидался ErrComponentOverPrincipal", err)
		}
		if out != nil {
			t.Error("при отказе отдан контекст")
		}
	})

	t.Run("тот же инициатор — тот же контекст", func(t *testing.T) {
		in := operations.WithPrincipal(context.Background(), operations.Principal{Type: "user", ID: "system.nlb-free-ip-runner"})
		out, err := journaltx.AsComponent(in, "nlb", "free-ip-runner")
		if err != nil {
			t.Fatalf("повтор на проходе отвергнут: %v", err)
		}
		if out != in {
			t.Error("повтор на проходе отдал другой контекст")
		}
	})

	over := map[string]operations.Principal{
		"пользователь":         {Type: "user", ID: ids.NewID(ids.PrefixUser)},
		"{system, bootstrap}":  operations.SystemPrincipal(),
		"другой компонент":     auth.SystemPrincipalFor("storage", "reconciler"),
		"id рабочего процесса": {Type: "user", ID: "usr-A"},
	}
	for name, p := range over {
		t.Run("поверх: "+name, func(t *testing.T) {
			out, err := journaltx.AsComponent(operations.WithPrincipal(context.Background(), p), "nlb", "free-ip-runner")
			if !errors.Is(err, journaltx.ErrComponentOverPrincipal) {
				t.Fatalf("отказ %v, ожидался ErrComponentOverPrincipal", err)
			}
			if out != nil {
				t.Error("при отказе отдан контекст")
			}
		})
	}

	unnamed := map[string][2]string{
		"пустая роль":   {"nlb", ""},
		"пустая служба": {"", "free-ip-runner"},
		"обе пусты":     {"", ""},
	}
	for name, pair := range unnamed {
		t.Run(name, func(t *testing.T) {
			out, err := journaltx.AsComponent(context.Background(), pair[0], pair[1])
			if !errors.Is(err, journaltx.ErrComponentUnnamed) {
				t.Fatalf("отказ %v, ожидался ErrComponentUnnamed", err)
			}
			if errors.Is(err, journaltx.ErrComponentOverPrincipal) {
				t.Errorf("отказ неназванной пары неотличим от отказа поверх принципала")
			}
			if out != nil {
				t.Error("при отказе отдан контекст")
			}
		})
	}

	t.Run("имя пары не DNS-метка", func(t *testing.T) {
		out, err := journaltx.AsComponent(context.Background(), "NLB", "free_ip")
		if err == nil || out != nil {
			t.Fatalf("пара, чей инициатор не выразим, принята: ctx=%v err=%v", out, err)
		}
		if !errors.Is(err, auth.ErrNoInitiator) {
			t.Errorf("отказ %v не несёт auth.ErrNoInitiator", err)
		}
	})
}
