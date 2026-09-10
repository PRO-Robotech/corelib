// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package quotadetail_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/corelib/quota/quotadetail"
)

// Разбор величин, посчитанных единственным производителем отказа учёта.
//
// Задача продукта #1605. Производитель (`kacho_quota_refuse`, рендер
// `pkg/quota/refusal.sql.tmpl`) кладёт величины в `DETAIL` объектом JSON; до
// этой работы они терялись на первом же переходе в Go, и клиент, получив 429,
// не мог машинно узнать ни предела, ни занятого, ни носителя.

// errSentinel — заменитель sentinel'а владельца: у каждого он свой, а свойство,
// которое здесь проверяется, — общее.
var errSentinel = errors.New("quota exceeded")

func TestDecode_FullBand(t *testing.T) {
	// Дословно то, что производит `jsonb_build_object(...)::text` полосы KQ001.
	const detail = `{"kind": "vpc.network", "used": 4, "limit": 4, ` +
		`"carrier_id": "prj-1", "carrier_type": "project"}`

	d, ok := quotadetail.Decode(detail)
	require.True(t, ok, "величины полосы KQ001 обязаны разбираться")

	assert.Equal(t, "project", d.CarrierType)
	assert.Equal(t, "prj-1", d.CarrierID)
	assert.Equal(t, "vpc.network", d.Kind)
	require.NotNil(t, d.Limit, "предел назван — значит доезжает")
	assert.Equal(t, int64(4), *d.Limit)
	require.NotNil(t, d.Used)
	assert.Equal(t, int64(4), *d.Used)

	assert.Equal(t, map[string]string{
		"carrier_type": "project",
		"carrier_id":   "prj-1",
		"kind":         "vpc.network",
		"limit":        "4",
		"used":         "4",
	}, d.Metadata())
}

// Полоса «потолок не назван» несёт ТРИ величины из пяти, и это не дефект: предел
// не назван, занятого у неназванного предела не существует.
func TestDecode_NoCeilingBandCarriesThreeOfFive(t *testing.T) {
	const detail = `{"kind": "vpc.gateway", "carrier_id": "prj-1", "carrier_type": "project"}`

	d, ok := quotadetail.Decode(detail)
	require.True(t, ok)

	assert.Nil(t, d.Limit, "предел не назван — значения нет, а не ноль")
	assert.Nil(t, d.Used)
	assert.Equal(t, map[string]string{
		"carrier_type": "project",
		"carrier_id":   "prj-1",
		"kind":         "vpc.gateway",
	}, d.Metadata(), "ключа неназванной величины в метаданных быть не должно")
}

// НЕСУЩЕЕ различие: ноль — законная величина занятого, и он обязан быть отличим
// от «величины нет». Свести их к одному значило бы сообщить арендатору, что он
// занял ноль мест, там, где мы просто не знаем.
func TestDecode_ZeroIsAValueNotAnAbsence(t *testing.T) {
	const detail = `{"carrier_type": "project", "carrier_id": "prj-1", ` +
		`"kind": "vpc.network", "limit": 0, "used": 0}`

	d, ok := quotadetail.Decode(detail)
	require.True(t, ok)

	require.NotNil(t, d.Used, "ноль назван — значит величина есть")
	assert.Equal(t, int64(0), *d.Used)
	assert.Equal(t, "0", d.Metadata()["used"])
	assert.Equal(t, "0", d.Metadata()["limit"])
}

// Словарь ЗАКРЫТ: ключ, которого производитель отказа учёта не объявлял, наружу
// не уходит. Это не косметика — `DETAIL` заполняет и сам Postgres на нарушениях
// ограничений, вписывая туда значения строки; открытый проброс сделал бы из
// метаданных неконтролируемый канал наружу.
func TestDecode_UnknownKeysNeverReachTheClient(t *testing.T) {
	const detail = `{"carrier_type": "project", "carrier_id": "prj-1", ` +
		`"kind": "vpc.network", "limit": 4, "used": 4, ` +
		`"internal_table": "kacho_vpc.networks", "row": "secret"}`

	d, ok := quotadetail.Decode(detail)
	require.True(t, ok)

	md := d.Metadata()
	assert.NotContains(t, md, "internal_table")
	assert.NotContains(t, md, "row")
	assert.Len(t, md, 5, "ровно пять объявленных ключей, ни одного лишнего")
}

// Негодная или отсутствующая DETAIL — НЕ отказ: величин просто нет, а отказ по
// пределу остаётся отказом по пределу.
func TestDecode_UnusableDetailIsNotAFailure(t *testing.T) {
	for name, detail := range map[string]string{
		"пусто":              "",
		"не JSON":            "project prj-1 has reached its limit",
		"JSON, но не объект": `["project", "prj-1"]`,
		"объект без величин": `{"unrelated": 1}`,
	} {
		t.Run(name, func(t *testing.T) {
			d, ok := quotadetail.Decode(detail)
			assert.False(t, ok, "величин нет — и это законный исход")
			assert.Empty(t, d.Metadata())
		})
	}
}

// Обёртка ПРОЗРАЧНА: текст отказа — часть контракта, и она его не трогает.
// Свойство держится построением (Error() отдаёт текст вложенного), а не
// договорённостью.
func TestAttach_PreservesTextAndSentinelExactly(t *testing.T) {
	const producer = "project prj-1 has reached its limit of 4 vpc.network"
	inner := fmt.Errorf("%w: %s", errSentinel, producer)

	out := quotadetail.Attach(inner, `{"carrier_type": "project", "carrier_id": "prj-1", `+
		`"kind": "vpc.network", "limit": 4, "used": 4}`)

	assert.Equal(t, inner.Error(), out.Error(), "текст производителя — контракт, он не меняется")
	assert.True(t, errors.Is(out, errSentinel), "sentinel обязан продолжать распознаваться")

	d, ok := quotadetail.FromError(out)
	require.True(t, ok, "величины обязаны доставаться из цепочки")
	assert.Equal(t, "prj-1", d.CarrierID)
}

// Нечего приклеивать — ошибка возвращается ТОЙ ЖЕ, а не завёрнутой в пустышку:
// пустая обёртка была бы утверждением «величины есть» при их отсутствии.
func TestAttach_WithoutUsableDetailReturnsTheErrorUnchanged(t *testing.T) {
	inner := fmt.Errorf("%w: project prj-1 has no ceiling stated for vpc.gateway", errSentinel)

	out := quotadetail.Attach(inner, "")

	assert.Equal(t, inner, out, "негодная DETAIL не заводит обёртки")
	_, ok := quotadetail.FromError(out)
	assert.False(t, ok)
}

// Положительный контроль к отрицанию выше: на ошибке без величин FromError
// обязан отвечать «нет», а не выдумывать нули.
func TestFromError_PlainErrorCarriesNothing(t *testing.T) {
	d, ok := quotadetail.FromError(fmt.Errorf("%w: whatever", errSentinel))

	assert.False(t, ok)
	assert.Nil(t, d.Limit)
	assert.Nil(t, d.Used)
	assert.Empty(t, d.Metadata())
}

func TestAttach_NilErrorStaysNil(t *testing.T) {
	assert.NoError(t, quotadetail.Attach(nil, `{"carrier_type": "project"}`))
}

// MetadataFromError — то, что зовёт сборка ответа у каждого владельца. Проверяются
// ОБА исхода: величины есть и величин нет.
func TestMetadataFromError(t *testing.T) {
	withAmounts := quotadetail.Attach(
		fmt.Errorf("%w: project prj-1 has reached its limit of 4 vpc.network", errSentinel),
		`{"carrier_type": "project", "carrier_id": "prj-1", "kind": "vpc.network", `+
			`"limit": 4, "used": 4}`)

	assert.Equal(t, map[string]string{
		"carrier_type": "project",
		"carrier_id":   "prj-1",
		"kind":         "vpc.network",
		"limit":        "4",
		"used":         "4",
	}, quotadetail.MetadataFromError(withAmounts))

	// Положительный контроль к отрицанию: без величин — nil, а не пустая карта
	// и не выдуманные нули.
	assert.Nil(t, quotadetail.MetadataFromError(fmt.Errorf("%w: whatever", errSentinel)))
	assert.Nil(t, quotadetail.MetadataFromError(nil))
}
