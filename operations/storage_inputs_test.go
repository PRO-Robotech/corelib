// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package operations_test

import (
	"fmt"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

// Перепись входов хранилища (приёмка NTF-5, DoD 10.1 п.2а (б); замысел З2 п.8).
//
// Утверждение: хранилище, методами которого служат Ф1…Ф4, получает извне только
// то, что названо допустимым набором, — ни одна экспортируемая функция,
// возвращающая хранилище, и ни один экспортируемый метод хранилища не принимает
// писателя другой таблицы, обработчика терминальной записи, флага или опции.
// Без такого входа у Ф1…Ф4 нет конфигурации, кроме той, что собирает
// NewRepo(pool, schema), и обвязка NTF5-122 (з) — единственная конфигурация.
//
// Находка — параметр функционального типа, интерфейса вне набора, логический,
// срез (в том числе вариадическая опция конструктора) и любой иной тип вне
// набора; печатается с именем функции и параметра.

// txWriterMethodNames — методы, объявление которых делает тип «хранилищем Ф1…Ф4».
var txWriterMethodNames = []string{"CreatePendingTx", "CreateDoneTx", "MarkDoneTx", "MarkErrorTx"}

// storageInterfaceNames — интерфейсы хранилища самого пакета: результат такого
// типа — хранилище, а параметр такого типа — сужение уже собранного хранилища.
var storageInterfaceNames = map[string]bool{"Repo": true, "FullRepo": true, "OwnedOperationRepo": true}

// allowedNamedParams — допустимые именованные типы параметров, «путь.Имя».
var allowedNamedParams = map[string]bool{
	"context.Context":            true,
	"github.com/jackc/pgx/v5.Tx": true,
	"time.Duration":              true,
	"github.com/PRO-Robotech/corelib/operations.Operation":  true,
	"github.com/PRO-Robotech/corelib/operations.Principal":  true,
	"github.com/PRO-Robotech/corelib/operations.Owner":      true,
	"github.com/PRO-Robotech/corelib/operations.ListFilter": true,
}

// allowedPointerParams — допустимые указатели, «путь.Имя» элемента.
var allowedPointerParams = map[string]bool{
	"github.com/jackc/pgx/v5/pgxpool.Pool":                    true,
	"google.golang.org/protobuf/types/known/anypb.Any":        true,
	"google.golang.org/genproto/googleapis/rpc/status.Status": true,
}

func namedPath(n *types.Named) string {
	if n.Obj().Pkg() == nil {
		return n.Obj().Name()
	}
	return n.Obj().Pkg().Path() + "." + n.Obj().Name()
}

// paramAllowed — входит ли тип параметра в допустимый набор.
func paramAllowed(pkg *packages.Package, t types.Type) bool {
	switch x := t.(type) {
	case *types.Basic:
		return x.Kind() == types.String || x.Info()&types.IsInteger != 0
	case *types.Alias:
		return paramAllowed(pkg, types.Unalias(x))
	case *types.Named:
		if x.Obj().Pkg() == pkg.Types && storageInterfaceNames[x.Obj().Name()] {
			return true
		}
		return allowedNamedParams[namedPath(x)]
	case *types.Pointer:
		if n, ok := x.Elem().(*types.Named); ok {
			return allowedPointerParams[namedPath(n)]
		}
	}
	return false
}

// returnsStorage — возвращает ли функция хранилище: Repo, FullRepo либо тип,
// объявляющий любую из Ф1…Ф4.
func returnsStorage(pkg *packages.Package, sig *types.Signature) bool {
	for i := 0; i < sig.Results().Len(); i++ {
		rt := sig.Results().At(i).Type()
		if n, ok := rt.(*types.Named); ok && n.Obj().Pkg() == pkg.Types &&
			(n.Obj().Name() == "Repo" || n.Obj().Name() == "FullRepo") {
			return true
		}
		ms := types.NewMethodSet(rt)
		if _, isPtr := rt.(*types.Pointer); !isPtr {
			if _, isIface := rt.Underlying().(*types.Interface); !isIface {
				ms = types.NewMethodSet(types.NewPointer(rt))
			}
		}
		for _, m := range txWriterMethodNames {
			if ms.Lookup(pkg.Types, m) != nil {
				return true
			}
		}
	}
	return false
}

type inputsReport struct {
	ctors    []string
	methods  []string
	params   int
	findings []string
}

func judgeParams(pkg *packages.Package, label string, sig *types.Signature, rep *inputsReport) {
	for i := 0; i < sig.Params().Len(); i++ {
		p := sig.Params().At(i)
		rep.params++
		if !paramAllowed(pkg, p.Type()) {
			rep.findings = append(rep.findings,
				fmt.Sprintf("%s: параметр %s типа %s вне допустимого набора", label, p.Name(),
					types.TypeString(p.Type(), types.RelativeTo(pkg.Types))))
		}
	}
}

// storageInputs перечисляет экспортируемые функции, возвращающие хранилище, и
// экспортируемые методы типа хранилища с их параметрами. Пустой обход — ошибка.
func storageInputs(pkg *packages.Package, storage string) (inputsReport, error) {
	var rep inputsReport
	named, err := storageNamed(pkg, storage)
	if err != nil {
		return rep, err
	}

	scope := pkg.Types.Scope()
	for _, name := range scope.Names() {
		fn, ok := scope.Lookup(name).(*types.Func)
		if !ok || !fn.Exported() {
			continue
		}
		sig, ok := fn.Type().(*types.Signature)
		if !ok || !returnsStorage(pkg, sig) {
			continue
		}
		rep.ctors = append(rep.ctors, fn.Name())
		judgeParams(pkg, fn.Name(), sig, &rep)
	}

	ms := types.NewMethodSet(types.NewPointer(named))
	for i := 0; i < ms.Len(); i++ {
		m, ok := ms.At(i).Obj().(*types.Func)
		if !ok || !m.Exported() {
			continue
		}
		rep.methods = append(rep.methods, m.Name())
		sig, ok := m.Type().(*types.Signature)
		if !ok {
			return rep, fmt.Errorf("метод %s без сигнатуры", m.Name())
		}
		judgeParams(pkg, storage+"."+m.Name(), sig, &rep)
	}

	if len(rep.ctors) == 0 || len(rep.methods) == 0 {
		return rep, fmt.Errorf("пустой обход: функций, возвращающих хранилище, %d; методов %d",
			len(rep.ctors), len(rep.methods))
	}
	sort.Strings(rep.findings)
	return rep, nil
}

// Близнец: на дереве правки перепись молчит; объём — одна функция, возвращающая
// хранилище (NewRepo), и 17 экспортируемых методов (13 прежних и Ф1…Ф4).
func TestStorageInputs_CensusIsSilentOnTheTree(t *testing.T) {
	rep, err := storageInputs(loadOperations(t, nil), "pgRepo")
	require.NoError(t, err)
	t.Logf("перепись входов хранилища: функций, возвращающих хранилище, %d (%s); методов %d (%s); параметров %d; находок %d",
		len(rep.ctors), strings.Join(rep.ctors, ", "), len(rep.methods), strings.Join(rep.methods, ", "),
		rep.params, len(rep.findings))
	require.Equal(t, []string{"NewRepo"}, rep.ctors)
	require.Len(t, rep.methods, 17)
	for _, m := range txWriterMethodNames {
		require.Contains(t, rep.methods, m)
	}
	require.Empty(t, rep.findings)
}

// Инъекция: параметр «писатель строки при терминальной записи» в Ф4 — красный с
// именем метода и параметра.
func TestStorageInputs_InjectedTerminalWriterParamOnMarkErrorTxIsFound(t *testing.T) {
	ov := injectInto(t, nil, "txwriter.go",
		"MarkErrorTx(ctx context.Context, tx pgx.Tx, id string, st *status.Status) error",
		"MarkErrorTx(ctx context.Context, tx pgx.Tx, id string, st *status.Status, onTerminal func(context.Context, pgx.Tx) error) error")
	rep, err := storageInputs(loadOperations(t, ov), "pgRepo")
	require.NoError(t, err)
	t.Logf("инъекция: находок %d: %s", len(rep.findings), strings.Join(rep.findings, "; "))
	require.Len(t, rep.findings, 1)
	require.Contains(t, rep.findings[0], "pgRepo.MarkErrorTx: параметр onTerminal")
}

// Инъекция: опция конструктора «писатель ленты» — красный с именем функции и
// параметра.
func TestStorageInputs_InjectedConstructorOptionIsFound(t *testing.T) {
	ov := injectInto(t, nil, "repo.go",
		"func NewRepo(pool *pgxpool.Pool, schema string) TxRepo {",
		"func NewRepo(pool *pgxpool.Pool, schema string, opts ...RepoOption) TxRepo {")
	ov = addFile(t, ov, "zz_injected_option.go",
		"package operations\n\nimport (\n\t\"context\"\n\n\t\"github.com/jackc/pgx/v5\"\n)\n\n"+
			"// RepoOption — инъекция пробы: опция, подающая писателя ленты.\n"+
			"type RepoOption func(feedWriter func(context.Context, pgx.Tx) error)\n")
	rep, err := storageInputs(loadOperations(t, ov), "pgRepo")
	require.NoError(t, err)
	t.Logf("инъекция: находок %d: %s", len(rep.findings), strings.Join(rep.findings, "; "))
	require.Len(t, rep.findings, 1)
	require.Contains(t, rep.findings[0], "NewRepo: параметр opts")
}

// Пустой обход — красный.
func TestStorageInputs_EmptyWalkIsNotAVerdict(t *testing.T) {
	pkg := loadOperations(t, nil)
	_, err := storageInputs(pkg, "noSuchStorage")
	require.ErrorContains(t, err, "не найден")
	// Тип без экспортируемых методов: методов 0 — «не проверили», а не «чисто».
	_, err = storageInputs(pkg, "rowQuerier")
	require.ErrorContains(t, err, "пустой обход")
}
