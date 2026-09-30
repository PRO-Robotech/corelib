// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// bound_test.go — форма ScopeBound (З14): объект проверки есть экземпляр типа,
// к которому процесс привязал сервер, а не объект, названный запросом.
//
// Каждое отрицание стоит в паре с законным близнецом: отказ вывода — рядом с
// выводом законной формы того же типа и отношения, отказ привязки — рядом с
// привязкой, прошедшей на той же карте.
package catalogderive_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	// Настоящий контракт ленты — единственный сегодняшний производитель формы.
	_ "github.com/PRO-Robotech/corelib/api/corelib/notify"

	"github.com/PRO-Robotech/corelib/authz"
	"github.com/PRO-Robotech/corelib/authz/catalogderive"
)

const (
	notifyPackage     = "corelib.notify"
	notifyClaim       = "/corelib.notify.InternalNotificationFeedService/Claim"
	notifyAck         = "/corelib.notify.InternalNotificationFeedService/Ack"
	notifyFeedType    = "notification_feed"
	notifyProbeModule = "notify-probe"
)

// TestAnnotationsReadTheBoundToServerField — поле аннотации читается; без этого
// гейт «каталог == аннотации» сверял бы форму, которой не видит.
func TestAnnotationsReadTheBoundToServerField(t *testing.T) {
	var seen, bound int
	catalogderive.RangeAnnotated([]string{boundPackage}, func(_ string, _ protoreflect.MethodDescriptor, a catalogderive.Annotations) {
		seen++
		if a.ScopeBoundToServer {
			bound++
		}
	})
	require.Equal(t, 2, seen, "фикстура несёт два метода — обход прочитал не её")
	assert.Equal(t, 2, bound, "оба метода фикстуры несут bound_to_server")
}

// TestDeriveCarriesTheScopeBoundForm — законная форма выводится: отношение и тип
// с аннотации, идентификатора нет — его принесёт привязка.
func TestDeriveCarriesTheScopeBoundForm(t *testing.T) {
	m, err := catalogderive.Derive(boundPackage)
	require.NoError(t, err)
	require.Len(t, m, catalogderive.MethodCount(boundPackage))

	for _, key := range []string{boundClaim, boundAck} {
		e, ok := m[key]
		require.True(t, ok, "метод %s обязан попасть в карту", key)
		assert.Equal(t, "reader", e.Relation, key)
		assert.Equal(t, probeFeedType, e.BoundType, key)
		assert.False(t, e.Public, key)
		assert.False(t, e.ScopeFiltered, key)
		assert.Nil(t, e.Extract, "%s: идентификатор экземпляра знает корень, а не аннотация", key)
	}
	assert.Equal(t, "probe.feed.claim", m[boundClaim].Permission)
}

// TestDeriveRefusesBoundTogetherWithARequestField — `bound_to_server` вместе с
// полем запроса невыразимо: одна аннотация назвала бы два объекта проверки.
// Отказ называет метод, поле и форму.
func TestDeriveRefusesBoundTogetherWithARequestField(t *testing.T) {
	for _, c := range []struct {
		pkg, method, field string
	}{
		{boundWithFieldPackage, boundWithFieldMethod, "from_request_field"},
		{boundWithTypeFieldPackage, boundWithTypeFieldMethod, "object_type_from_request_field"},
		{boundExemptPackage, boundExemptMethod, "<exempt>"},
	} {
		t.Run(c.field, func(t *testing.T) {
			_, err := catalogderive.Derive(c.pkg)
			require.Error(t, err, "аннотация, называющая два объекта, выведена молча")
			assert.Contains(t, err.Error(), c.method, "отказ обязан назвать метод")
			assert.Contains(t, err.Error(), "bound_to_server")
			assert.Contains(t, err.Error(), c.field)
		})
	}
}

// TestDeriveReadsTheFeedContract — настоящий контракт ленты выводится, и оба его
// метода — формы ScopeBound на типе ленты. До формы его вывод отказывал на
// пустом `from_request_field`, и процесс с сервером ленты не поднимался.
func TestDeriveReadsTheFeedContract(t *testing.T) {
	m, err := catalogderive.Derive(notifyPackage)
	require.NoError(t, err)
	require.Len(t, m, catalogderive.MethodCount(notifyPackage))
	require.NotZero(t, len(m), "контракт ленты не слинкован — проба ничего не осмотрела")

	for _, key := range []string{notifyClaim, notifyAck} {
		e, ok := m[key]
		require.True(t, ok, key)
		assert.Equal(t, notifyFeedType, e.BoundType, key)
		assert.Equal(t, "reader", e.Relation, key)
	}
}

// TestBindFillsTheBoundExtractor — привязка даёт каждому методу своего типа
// объект `тип:идентификатор`, не читая запроса.
func TestBindFillsTheBoundExtractor(t *testing.T) {
	m, err := catalogderive.Derive(notifyPackage)
	require.NoError(t, err)

	bound, err := catalogderive.Bind(m, map[string]string{notifyFeedType: notifyProbeModule})
	require.NoError(t, err)

	for _, key := range []string{notifyClaim, notifyAck} {
		e := bound[key]
		require.NotNil(t, e.Extract, "%s: привязка не заполнила извлекатель", key)
		ot, id, xerr := e.Extract(nil)
		require.NoError(t, xerr)
		assert.Equal(t, notifyFeedType, ot, key)
		assert.Equal(t, notifyProbeModule, id, key)

		// Тип отвечает тем же, чем его спрашивает перепись скрытия существования.
		st, ok := catalogderive.ScopeObjectType(key, e)
		require.True(t, ok, key)
		assert.Equal(t, notifyFeedType, st)
	}
	assert.Nil(t, m[notifyClaim].Extract, "привязка не правит выведенную карту на месте")
}

// TestBindRefusesABoundMethodWithoutItsBinding — метод формы, для типа которого
// привязки нет, — отказ с именем метода и типа. Молча оставленный пустым, он
// отвергался бы на каждом вызове голосом прав.
func TestBindRefusesABoundMethodWithoutItsBinding(t *testing.T) {
	m, err := catalogderive.Derive(boundPackage)
	require.NoError(t, err)

	_, err = catalogderive.Bind(m, nil)
	require.Error(t, err, "метод без привязки прошёл")
	assert.Contains(t, err.Error(), boundClaim)
	assert.Contains(t, err.Error(), boundAck, "отказ называет КАЖДЫЙ непривязанный метод")
	assert.Contains(t, err.Error(), probeFeedType)

	_, err = catalogderive.Bind(m, map[string]string{"other_feed": "x"})
	require.Error(t, err, "привязка чужого типа закрыла метод")
	assert.Contains(t, err.Error(), boundClaim)
}

// TestBindRefusesABindingNoMethodReads — привязка, которую не читает ни один
// метод, — проводка без предмета: сервер привязан, а карта его формы не несёт.
func TestBindRefusesABindingNoMethodReads(t *testing.T) {
	m, err := catalogderive.Derive(probePackage)
	require.NoError(t, err)

	_, err = catalogderive.Bind(m, map[string]string{probeFeedType: "feed-a"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), probeFeedType)
}

// TestBindRefusesAnIdentifierThatFormsNoObject — идентификатор, которым нельзя
// назвать объект модели, отвергается при привязке, а не на каждом вызове.
func TestBindRefusesAnIdentifierThatFormsNoObject(t *testing.T) {
	m, err := catalogderive.Derive(boundPackage)
	require.NoError(t, err)

	for _, id := range []string{"", "feed:a", "feed#member"} {
		_, err := catalogderive.Bind(m, map[string]string{probeFeedType: id})
		require.Error(t, err, "идентификатор %q принят", id)
		assert.Contains(t, err.Error(), probeFeedType)
	}
}

// TestBindLeavesOtherFormsAlone — близнец: карта без формы ScopeBound и без
// привязок проходит, и ни одна запись не меняется.
func TestBindLeavesOtherFormsAlone(t *testing.T) {
	m, err := catalogderive.Derive(probePackage)
	require.NoError(t, err)

	bound, err := catalogderive.Bind(m, nil)
	require.NoError(t, err)
	require.Len(t, bound, len(m))
	for k, e := range m {
		b := bound[k]
		assert.Equal(t, e.Relation, b.Relation, k)
		assert.Equal(t, e.Public, b.Public, k)
		assert.Equal(t, e.ScopeFiltered, b.ScopeFiltered, k)
		assert.Equal(t, e.Extract == nil, b.Extract == nil, k)
		assert.Empty(t, b.BoundType, k)
	}
	var _ authz.RPCMap = bound
}
