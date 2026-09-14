// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// derive_test.go — вывод карты прав из аннотаций, на НЕЙТРАЛЬНОМ дескрипторе.
//
// Дескриптор строит и регистрирует `probefixture_test.go`; там же разобрано,
// почему он нейтральный, а не доменный (задача #2532, класс 1). Здесь — только
// утверждения о полосах вывода.
//
// # Что изменилось вместе с дескриптором, а что нет
//
// Полос вывода по-прежнему шесть, и представитель каждой на месте. Изменились
// ИМЕНА, которыми проба его называет, — и это ровно то, ради чего замена сделана:
// имя доменного метода приносило с собой чужие решения (какой RPC кластерный,
// какая полоса у чтения каталога), и проба краснела на их изменении, не на своём
// предмете. Так уже было: одна из проб ниже переезжала с `List` на `Create`,
// когда полосу чтения каталога типов дисков исправили.
//
// Полоса `<exempt>` осталась на настоящем контракте — `kacho.cloud.operation`
// живёт в ФУНДАМЕНТЕ, ребра к платформе не образует, и подделывать её нечем:
// это единственная полоса, чей представитель у фундамента свой.
package catalogderive_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/PRO-Robotech/corelib/api/corelib/api/v1"

	// Контракт операции линкуется ЯВНО. Прежде его дескрипторы приезжали
	// транзитивно, вместе со стабами трёх доменов платформы: каждый из них
	// импортирует тип операции. Со снятием тех стабов транзитивный путь исчез, и
	// полоса `<exempt>` осталась бы без своего представителя — то есть проба
	// зеленела бы на пустой карте. Ребра к платформе импорт не образует:
	// `pkg/api/kacho/cloud/operation` объявлен классом `corelib`.
	_ "github.com/PRO-Robotech/corelib/api/kacho/cloud/operation"

	"github.com/PRO-Robotech/corelib/authz"
	"github.com/PRO-Robotech/corelib/authz/catalogderive"
)

// TestDeriveBuildsTheEdgeCheckingEntry — обычная строка: отношение, тип объекта и
// идентификатор, взятый из названного аннотацией поля запроса.
func TestDeriveBuildsTheEdgeCheckingEntry(t *testing.T) {
	m, err := catalogderive.Derive(probePackage)
	require.NoError(t, err)

	e, ok := m[probeGet]
	require.True(t, ok, "метод пакета обязан попасть в выведенную карту")
	assert.Equal(t, "v_get", e.Relation)
	assert.Equal(t, "probe.things.get", e.Permission)
	assert.False(t, e.Public)
	assert.False(t, e.ScopeFiltered)

	require.NotNil(t, e.Extract)
	ot, id, xerr := e.Extract(newProbeRequest(t, "GetThingRequest", map[string]string{"thing_id": "thg-1"}))
	require.NoError(t, xerr)
	assert.Equal(t, probeThingType, ot)
	assert.Equal(t, "thg-1", id, "идентификатор берётся из поля, названного аннотацией")
}

// TestDeriveReadsTheParentScopeField — область берётся с родительского якоря,
// когда аннотация называет его поле.
func TestDeriveReadsTheParentScopeField(t *testing.T) {
	m, err := catalogderive.Derive(probePackage)
	require.NoError(t, err)

	e := m[probeCreate]
	assert.Equal(t, "editor", e.Relation)
	require.NotNil(t, e.Extract)
	ot, id, xerr := e.Extract(newProbeRequest(t, "CreateThingRequest", map[string]string{"holder_id": "prj-7"}))
	require.NoError(t, xerr)
	assert.Equal(t, "project", ot)
	assert.Equal(t, "prj-7", id)
}

// TestDeriveSubstitutesTheClusterSingleton — якорь на кластере адресуется
// синглтоном, ровно как это делает край; иначе Check уходит на `cluster:*`,
// который отвергается как unscoped.
func TestDeriveSubstitutesTheClusterSingleton(t *testing.T) {
	m, err := catalogderive.Derive(probePackage)
	require.NoError(t, err)

	e := m[probeSingleton]
	require.NotNil(t, e.Extract)
	ot, id, xerr := e.Extract(newProbeRequest(t, "CreateSingletonRequest", nil))
	require.NoError(t, xerr)
	assert.Equal(t, "cluster", ot)
	assert.Equal(t, "cluster_root", id)
}

// TestDeriveCarriesTheScopeFilteredLane — полоса «сужает владелец» переносится
// как ScopeFiltered, а НЕ как Public: разница в том, требуется ли субъект.
func TestDeriveCarriesTheScopeFilteredLane(t *testing.T) {
	m, err := catalogderive.Derive(probePackage)
	require.NoError(t, err)

	e := m[probeScopeFilter]
	assert.True(t, e.ScopeFiltered, "строка scope_filtered обязана требовать субъекта")
	assert.False(t, e.Public)
	assert.Empty(t, e.Relation)
	assert.Nil(t, e.Extract)
}

// TestDeriveCarriesTheExemptLane — `<exempt>` снимает per-RPC Check целиком.
//
// Единственная полоса на НАСТОЯЩЕМ контракте: `kacho.cloud.operation` — контракт
// фундамента, и ребра к платформе он не образует.
func TestDeriveCarriesTheExemptLane(t *testing.T) {
	m, err := catalogderive.Derive("kacho.cloud.operation")
	require.NoError(t, err)

	e, ok := m["/kacho.cloud.operation.OperationService/Get"]
	require.True(t, ok)
	assert.True(t, e.Public)
	assert.False(t, e.ScopeFiltered)
	assert.Empty(t, e.Relation)
	assert.Nil(t, e.Extract)
}

// TestDeriveCarriesHideExistence — форма отказа переносится с той же строки.
func TestDeriveCarriesHideExistence(t *testing.T) {
	m, err := catalogderive.Derive(probePackage)
	require.NoError(t, err)

	var hiding int
	for _, e := range m {
		if e.HideExistence {
			hiding++
		}
	}
	assert.Equal(t, 1, hiding,
		"скрывающих записей ровно одна — столько их в дескрипторе; ноль означает, "+
			"что аннотация перестала читаться, больше одной — что дескриптор изменили, "+
			"а проба тотальности этого не заметила")

	e := m[probeHidden]
	require.NotNil(t, e.Extract, "скрывающая строка остаётся пообъектной")
	assert.True(t, e.HideExistence)
}

// TestDeriveRefusesAnUnlinkedPackage — предпосылка вывода: названный пакет обязан
// быть в бинаре. Молчаливая пустая карта означала бы, что каждый RPC сервиса
// отвечает fail-closed, и узналось бы это первым запросом.
func TestDeriveRefusesAnUnlinkedPackage(t *testing.T) {
	const absent = "corelib.authz.nosuch.v1"
	_, err := catalogderive.Derive(probePackage, absent)
	require.Error(t, err)
	assert.Contains(t, err.Error(), absent)
}

// TestDeriveRefusesAnEmptyPackageList — вывод без единого пакета даёт пустую
// карту, а пустая карта — отказ на каждом RPC.
func TestDeriveRefusesAnEmptyPackageList(t *testing.T) {
	_, err := catalogderive.Derive()
	require.Error(t, err)
}

// TestExtractorRejectsAForeignRequest — экстрактор привязан к типу запроса
// СВОЕГО метода. Чужое сообщение не «даёт пустой id», оно отвергается: пустой id
// уехал бы в Check как `type:` и получил бы отказ, неотличимый от отказа по
// правам.
func TestExtractorRejectsAForeignRequest(t *testing.T) {
	m, err := catalogderive.Derive(probePackage)
	require.NoError(t, err)

	e := m[probeGet]
	require.NotNil(t, e.Extract)
	_, _, xerr := e.Extract(newProbeRequest(t, "CreateThingRequest", map[string]string{"holder_id": "prj-1"}))
	require.Error(t, xerr)
}

// TestDeriveIsTotalOverThePackage — выведенная карта покрывает КАЖДЫЙ метод
// названного пакета. Метод без записи отвечает fail-closed, и молчаливый пропуск
// был бы отказом, о котором никто не объявлял.
func TestDeriveIsTotalOverThePackage(t *testing.T) {
	m, err := catalogderive.Derive(probePackage)
	require.NoError(t, err)

	assert.Equal(t, catalogderive.MethodCount(probePackage), len(m),
		"в карте столько же записей, сколько методов у пакета")
	require.NotZero(t, len(m), "карта пуста — проба тотальности ничего не осмотрела")
	for k := range m {
		require.True(t, strings.HasPrefix(k, "/"+probePackage+"."),
			"в карту попал метод чужого пакета: %s", k)
	}
}

// TestEmptyScopeIdIsRefusedNotAsked — вызов, не назвавший область, отвергается,
// а не спрашивается с пустым идентификатором.
//
// Проба существует ради разобранного расхождения vpc `AddressService/GetByValue`
// (единственный RPC, чья область берётся из необязательного поля): рукописный
// экстрактор возвращал ошибку на пустом поле, выведенный возвращает пустой id.
// Утверждать, что «исход тот же», по прочтению кода — недостаточно; здесь он
// проверен: пустой id не образует объекта, а значит вопрос не задаётся и вызов
// отвергается.
func TestEmptyScopeIdIsRefusedNotAsked(t *testing.T) {
	m, err := catalogderive.Derive(probePackage)
	require.NoError(t, err)

	e := m[probeGet]
	require.NotNil(t, e.Extract)

	ot, id, xerr := e.Extract(newProbeRequest(t, "GetThingRequest", nil)) // поле не заполнено
	require.NoError(t, xerr)
	require.Equal(t, probeThingType, ot)
	require.Empty(t, id)

	_, ferr := authz.FormatObject(ot, id)
	require.Error(t, ferr, "пустой идентификатор не образует объекта — интерсептор отказывает")
}

// TestDeriveIsCompatibleWithTheInterceptorLookup — ключ выведенной карты обязан
// совпадать с тем, чем grpc-go зовёт метод.
func TestDeriveIsCompatibleWithTheInterceptorLookup(t *testing.T) {
	m, err := catalogderive.Derive(probePackage)
	require.NoError(t, err)

	var rm authz.RPCMap = m
	_, ok := rm.Lookup(probeDelete)
	assert.True(t, ok)
}

// TestDeriveResolvesADottedScopePath — область, лежащая внутри вложенного тела
// запроса, читается по составному пути.
//
// Прежде проба стояла на `vpc InternalAddressService.CreateOwnedAddress` —
// единственном таком сайте дерева, где внутренний путь намеренно переиспользует
// ЦЕЛИКОМ тело публичного создания. Форма сохранена дословно: вложенное сообщение
// того же типа, что и тело обычного создания, и область лежит на уровень глубже.
func TestDeriveResolvesADottedScopePath(t *testing.T) {
	m, err := catalogderive.Derive(probePackage)
	require.NoError(t, err)

	e, ok := m[probeDottedCreate]
	require.True(t, ok)
	assert.Equal(t, "editor", e.Relation)
	require.NotNil(t, e.Extract)

	ot, id, xerr := e.Extract(newProbeRequest(t, "CreateOwnedThingRequest",
		map[string]string{"inner.holder_id": "prj-42"}))
	require.NoError(t, xerr)
	assert.Equal(t, "project", ot)
	assert.Equal(t, "prj-42", id)

	// Пустое вложенное сообщение не «подставляет» ничего: id пуст, объект не
	// образуется, вызов отвергается.
	_, id2, xerr2 := e.Extract(newProbeRequest(t, "CreateOwnedThingRequest", nil))
	require.NoError(t, xerr2)
	assert.Empty(t, id2)
}
