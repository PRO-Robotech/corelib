// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package operations_test

import (
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

// Гейт замыкания Ф1…Ф4 (замысел NTF-5, З2 п.6–п.7; CX5-47).
//
// Утверждение: функции записи операции в транзакции вызывающего и каждая
// функция пакета, до которой они дотягиваются, не берут настройку мимо
// хранилища — не читают переменных уровня пакета (кроме сигнальных ошибок), не
// зовут ctx.Value ни прямо, ни через *FromContext, не читают окружение процесса,
// а из полей хранилища читают только schema и только через tableName. Тогда у
// Ф1…Ф4 нет ни писателя другой таблицы, ни флага, кроме того, что приходит
// входом хранилища, — а входы держит перепись storage_inputs_test.go.
//
// Узел графа — объект функции go/types, а не имя: одноимённая функция другого
// пакета или комментарий с именем в граф не попадают. Вызовы через интерфейс
// (pgx.Tx.QueryRow) — чужой пакет, в граф не идут: предмет гейта — настройка
// ЭТОГО пакета.

// txWriterRoots — Ф1…Ф4, корни обхода.
var txWriterRoots = []string{"CreatePendingTx", "CreateDoneTx", "MarkDoneTx", "MarkErrorTx"}

// closureAllowedVars — переменные уровня пакета, чтение которых законно:
// сигнальные ошибки, по которым вызывающий классифицирует исход.
var closureAllowedVars = map[string]bool{
	"ErrNotFound":       true,
	"ErrAlreadyDone":    true,
	"ErrNilTx":          true,
	"ErrEmptyPrincipal": true,
}

// loadOperations загружает пакет operations (без тестовых файлов) с типами.
// overlay подменяет содержимое файлов по абсолютному пути — так инъекция
// подаётся настоящим исходником пакета, а не синтетикой.
func loadOperations(t *testing.T, overlay map[string][]byte) *packages.Package {
	t.Helper()
	cfg := &packages.Config{
		Mode:    packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:     ".",
		Overlay: overlay,
	}
	pkgs, err := packages.Load(cfg, ".")
	require.NoError(t, err)
	require.Len(t, pkgs, 1)
	pkg := pkgs[0]
	require.Empty(t, pkg.Errors, "пакет operations обязан собираться — иначе гейт судил бы неполные типы")
	require.Equal(t, "github.com/PRO-Robotech/corelib/operations", pkg.PkgPath)
	return pkg
}

// injectInto возвращает overlay, в котором файл пакета file изменён заменой
// old → new. Замена, не попавшая ни разу, — отказ: инъекция без предмета держала
// бы ничего.
func injectInto(t *testing.T, overlay map[string][]byte, file, old, new string) map[string][]byte {
	t.Helper()
	if overlay == nil {
		overlay = map[string][]byte{}
	}
	abs, err := filepath.Abs(file)
	require.NoError(t, err)
	src, ok := overlay[abs]
	if !ok {
		src, err = os.ReadFile(abs)
		require.NoError(t, err)
	}
	require.True(t, strings.Contains(string(src), old), "инъекция не попала: в %s нет %q", file, old)
	overlay[abs] = []byte(strings.ReplaceAll(string(src), old, new))
	return overlay
}

// addFile добавляет в overlay новый файл пакета.
func addFile(t *testing.T, overlay map[string][]byte, file, src string) map[string][]byte {
	t.Helper()
	abs, err := filepath.Abs(file)
	require.NoError(t, err)
	overlay[abs] = []byte(src)
	return overlay
}

// storageNamed находит тип хранилища по имени.
func storageNamed(pkg *packages.Package, name string) (*types.Named, error) {
	obj := pkg.Types.Scope().Lookup(name)
	if obj == nil {
		return nil, fmt.Errorf("тип хранилища %s в пакете не найден", name)
	}
	named, ok := obj.Type().(*types.Named)
	if !ok {
		return nil, fmt.Errorf("%s — не именованный тип", name)
	}
	return named, nil
}

type closureReport struct {
	funcs    []string // разобранные функции пакета
	findings []string
}

// txWriterClosure строит граф вызовов внутри пакета от методов roots типа
// хранилища и судит тело каждой достигнутой функции. Пустой обход — ошибка, а не
// «находок 0»: «не с чем сверить» значит «не проверили».
func txWriterClosure(pkg *packages.Package, storage string, roots []string) (closureReport, error) {
	var rep closureReport
	named, err := storageNamed(pkg, storage)
	if err != nil {
		return rep, err
	}

	decls := map[*types.Func]*ast.FuncDecl{}
	for _, f := range pkg.Syntax {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			if fn, ok := pkg.TypesInfo.Defs[fd.Name].(*types.Func); ok {
				decls[fn] = fd
			}
		}
	}

	var queue []*types.Func
	seen := map[*types.Func]bool{}
	for _, root := range roots {
		var found *types.Func
		for i := 0; i < named.NumMethods(); i++ {
			if m := named.Method(i); m.Name() == root {
				found = m
			}
		}
		if found == nil {
			return rep, fmt.Errorf("корень обхода %s.%s не найден", storage, root)
		}
		seen[found] = true
		queue = append(queue, found)
	}
	if len(queue) == 0 {
		return rep, fmt.Errorf("пустой обход: корней 0")
	}

	storageObj := named.Obj()
	for len(queue) > 0 {
		fn := queue[0]
		queue = queue[1:]
		fd, ok := decls[fn]
		if !ok {
			return rep, fmt.Errorf("функция пакета %s достигнута, но её объявления нет", fn.Name())
		}
		name := funcLabel(fn)
		rep.funcs = append(rep.funcs, name)

		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				sel := pkg.TypesInfo.Selections[x]
				if sel == nil || sel.Kind() != types.FieldVal {
					return true
				}
				recv := sel.Recv()
				if p, ok := recv.(*types.Pointer); ok {
					recv = p.Elem()
				}
				if rn, ok := recv.(*types.Named); ok && rn.Obj() == storageObj {
					if sel.Obj().Name() != "schema" || fn.Name() != "tableName" {
						rep.findings = append(rep.findings,
							fmt.Sprintf("%s: читает поле хранилища %s мимо tableName", name, sel.Obj().Name()))
					}
				}
			case *ast.Ident:
				switch o := pkg.TypesInfo.Uses[x].(type) {
				case *types.Var:
					if o.Pkg() == pkg.Types && o.Parent() == pkg.Types.Scope() && !closureAllowedVars[o.Name()] {
						rep.findings = append(rep.findings,
							fmt.Sprintf("%s: читает переменную пакета %s", name, o.Name()))
					}
				case *types.Func:
					if strings.HasSuffix(o.Name(), "FromContext") {
						rep.findings = append(rep.findings,
							fmt.Sprintf("%s: зовёт %s — настройка из контекста", name, o.Name()))
					}
					if o.Pkg() != nil {
						switch path := o.Pkg().Path(); {
						case path == "context" && o.Name() == "Value":
							rep.findings = append(rep.findings,
								fmt.Sprintf("%s: зовёт ctx.Value", name))
						case path == "os" && (o.Name() == "Getenv" || o.Name() == "LookupEnv" || o.Name() == "Environ"):
							rep.findings = append(rep.findings,
								fmt.Sprintf("%s: читает окружение процесса os.%s", name, o.Name()))
						}
					}
					if o.Pkg() == pkg.Types && !isInterfaceMethod(o) {
						origin := o.Origin()
						if !seen[origin] {
							seen[origin] = true
							queue = append(queue, origin)
						}
					}
				}
			}
			return true
		})
	}
	sort.Strings(rep.funcs)
	sort.Strings(rep.findings)
	return rep, nil
}

// isInterfaceMethod — метод интерфейса пакета (rowQuerier.QueryRow,
// execer.Exec): тела у него нет, вызов уходит в реализацию вызывающего — pgx.Tx
// или пул, то есть в чужой пакет, который предметом гейта не является.
func isInterfaceMethod(fn *types.Func) bool {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return false
	}
	_, isIface := sig.Recv().Type().Underlying().(*types.Interface)
	return isIface
}

func funcLabel(fn *types.Func) string {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return fn.Name()
	}
	recv := sig.Recv().Type()
	if p, ok := recv.(*types.Pointer); ok {
		recv = p.Elem()
	}
	if n, ok := recv.(*types.Named); ok {
		return n.Obj().Name() + "." + fn.Name()
	}
	return fn.Name()
}

// Близнец: на дереве правки гейт молчит и печатает объём осмотренного. Обход
// обязан дойти до тел CAS-помощников и построителя — иначе «находок 0» значило
// бы «не читал».
func TestTxWriterClosure_ReadsNoSettingPastTheStorage(t *testing.T) {
	rep, err := txWriterClosure(loadOperations(t, nil), "pgRepo", txWriterRoots)
	require.NoError(t, err)
	t.Logf("замыкание Ф1…Ф4: разобрано функций %d: %s; находок %d",
		len(rep.funcs), strings.Join(rep.funcs, ", "), len(rep.findings))
	for _, must := range []string{"markDoneCAS", "markErrorCAS", "insertOperationTx", "Principal.IsAnonymous", "pgRepo.tableName"} {
		require.Contains(t, rep.funcs, must, "обход не дошёл до %s", must)
	}
	require.Empty(t, rep.findings)
}

// Инъекция: чтение defaultRegistry в markErrorCAS — красный с именем функции.
func TestTxWriterClosure_InjectedPackageVarReadInMarkErrorCASIsFound(t *testing.T) {
	ov := injectInto(t, nil, "repo.go",
		"func markErrorCAS(ctx context.Context, q rowQuerier, table, id string, errStatus *status.Status) error {",
		"func markErrorCAS(ctx context.Context, q rowQuerier, table, id string, errStatus *status.Status) error {\n\t_ = defaultRegistry")
	rep, err := txWriterClosure(loadOperations(t, ov), "pgRepo", txWriterRoots)
	require.NoError(t, err)
	t.Logf("инъекция: находок %d: %s", len(rep.findings), strings.Join(rep.findings, "; "))
	require.Equal(t, []string{"markErrorCAS: читает переменную пакета defaultRegistry"}, rep.findings)
}

// Инъекция: PrincipalFromContext(ctx) в построителе — красный с именем функции.
func TestTxWriterClosure_InjectedFromContextInBuilderIsFound(t *testing.T) {
	ov := injectInto(t, nil, "txwriter.go",
		"\tmetaType, metaData, err := marshalAny(op.Metadata)",
		"\t_ = PrincipalFromContext(ctx)\n\tmetaType, metaData, err := marshalAny(op.Metadata)")
	rep, err := txWriterClosure(loadOperations(t, ov), "pgRepo", txWriterRoots)
	require.NoError(t, err)
	t.Logf("инъекция: находок %d: %s", len(rep.findings), strings.Join(rep.findings, "; "))
	require.NotEmpty(t, rep.findings)
	require.Contains(t, rep.findings, "insertOperationTx: зовёт PrincipalFromContext — настройка из контекста")
}

// Инъекция: ctx.Value в markDoneCAS — красный с именем функции.
func TestTxWriterClosure_InjectedCtxValueInMarkDoneCASIsFound(t *testing.T) {
	ov := injectInto(t, nil, "repo.go",
		"func markDoneCAS(ctx context.Context, q rowQuerier, table, id string, response *anypb.Any) error {",
		"func markDoneCAS(ctx context.Context, q rowQuerier, table, id string, response *anypb.Any) error {\n\t_ = ctx.Value(struct{}{})")
	rep, err := txWriterClosure(loadOperations(t, ov), "pgRepo", txWriterRoots)
	require.NoError(t, err)
	t.Logf("инъекция: находок %d: %s", len(rep.findings), strings.Join(rep.findings, "; "))
	require.Equal(t, []string{"markDoneCAS: зовёт ctx.Value"}, rep.findings)
}

// Пустой обход — красный, а не «находок 0».
func TestTxWriterClosure_EmptyWalkIsNotAVerdict(t *testing.T) {
	pkg := loadOperations(t, nil)
	_, err := txWriterClosure(pkg, "pgRepo", nil)
	require.ErrorContains(t, err, "пустой обход")
	_, err = txWriterClosure(pkg, "pgRepo", []string{"NoSuchMethodTx"})
	require.ErrorContains(t, err, "не найден")
}
