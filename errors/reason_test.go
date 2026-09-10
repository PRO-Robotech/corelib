// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package errors

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// infoOf извлекает единственный ErrorInfo из деталей статуса. Возвращает nil,
// когда детали нет — это ОТДЕЛЬНЫЙ значимый исход (XC-1 D2: отсутствие токена
// значимо), поэтому тест обязан уметь его отличить от «токен не тот».
func infoOf(t *testing.T, err error) *errdetails.ErrorInfo {
	t.Helper()
	st, ok := status.FromError(err)
	require.True(t, ok, "ожидался gRPC-статус")
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return info
		}
	}
	return nil
}

// Полосы закрытого словаря и их коды — контракт api-conventions.md
// §By-lane code-split. Таблица перечисляет ВСЕ пять: полоса, выпавшая из
// словаря, обязана уронить этот тест, а не тихо потерять читателя.
func TestLanesCarryTheirCanonicalTokenAndCode(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lane  Reason
		token string
		code  codes.Code
	}{
		{"format-check", ReasonInvalidResourceID, "INVALID_RESOURCE_ID", codes.InvalidArgument},
		{"direct-read", ReasonResourceNotFound, "RESOURCE_NOT_FOUND", codes.NotFound},
		{"peer-validate miss", ReasonPeerResourceMissing, "PEER_RESOURCE_MISSING", codes.FailedPrecondition},
		{"peer-validate state", ReasonPeerResourceState, "PEER_RESOURCE_STATE", codes.FailedPrecondition},
		{"peer-validate unavailable", ReasonPeerUnavailable, "PEER_UNAVAILABLE", codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.token, tc.lane.Token())
			require.Equal(t, tc.code, tc.lane.Code())
		})
	}
}

// Словарь ЗАКРЫТ и его размер — часть контракта: шестая полоса не может
// появиться незамеченной. Компилятор запрещает СОБРАТЬ шестой токен (поля
// неэкспортируемые), а этот тест ловит противоположное — что кто-то завёл
// шестое ЗНАЧЕНИЕ внутри пакета, не объявив его контрактом.
func TestLaneDictionaryIsClosedAtFive(t *testing.T) {
	require.Len(t, AllReasons(), 5, "словарь полос — ровно пять (XC-1 D2)")
	seen := map[string]bool{}
	for _, r := range AllReasons() {
		require.NotEmpty(t, r.Token(), "полоса без токена непредставима")
		require.False(t, seen[r.Token()], "токен %s объявлен дважды", r.Token())
		seen[r.Token()] = true
	}
}

// Нулевое значение типа собрать МОЖНО (Go это не запрещает), поэтому оно обязано
// быть безобидным: полоса без токена не эмитирует детали и не притворяется
// успехом. Иначе `var r Reason` дал бы отказ с пустым признаком — то есть
// молчащий токен, неотличимый от «токена нет».
func TestZeroLaneIsInertNotSilentlyValid(t *testing.T) {
	var zero Reason
	require.False(t, zero.IsDeclared())
	require.Empty(t, zero.Token())

	err := zero.Errf(PeerRef{Service: "vpc", ResourceType: "geo.zone", ResourceID: "z1"}, "boom")
	require.Equal(t, codes.Internal, status.Code(err),
		"необъявленная полоса не вправе выдать себя за полосу контракта")
	require.Nil(t, infoOf(t, err), "необъявленная полоса не эмитирует машинный признак")
}

// Форма эталона, поднятая в фундамент: код полосы + проза вызывающего +
// машинный признак в деталях (домен, тип ресурса, идентификатор).
func TestErrfBuildsLaneCodeCallerProseAndMachineToken(t *testing.T) {
	err := ReasonPeerResourceMissing.Errf(
		PeerRef{Service: "vpc", ResourceType: "geo.zone", ResourceID: "zone-a"},
		"unknown zone id '%s'", "zone-a")

	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.FailedPrecondition, st.Code())
	require.Equal(t, "unknown zone id 'zone-a'", st.Message(),
		"проза принадлежит вызывающему — фундамент её не переписывает")

	info := infoOf(t, err)
	require.NotNil(t, info)
	require.Equal(t, "PEER_RESOURCE_MISSING", info.GetReason())
	require.Equal(t, "vpc.kacho.cloud", info.GetDomain())
	require.Equal(t, "geo.zone", info.GetMetadata()["resource_type"])
	require.Equal(t, "zone-a", info.GetMetadata()["resource_id"])
}

// Пустой идентификатор НЕ едет в детали пустой строкой. Это несущее требование,
// а не косметика: на полосе, где проза намеренно генерическая (анти-oracle),
// метаданные не вправе подтвердить существование чужого ресурса. Пустое
// `resource_id` в карте читалось бы клиентом как «идентификатор известен и пуст».
func TestErrfOmitsEmptyResourceIDInsteadOfSendingBlank(t *testing.T) {
	err := ReasonPeerResourceMissing.Errf(
		PeerRef{Service: "nlb", ResourceType: "vpc.address"},
		"Illegal argument addressId")

	info := infoOf(t, err)
	require.NotNil(t, info)
	require.Equal(t, "vpc.address", info.GetMetadata()["resource_type"])
	_, present := info.GetMetadata()["resource_id"]
	require.False(t, present, "пустой идентификатор не эмитируется вовсе")
}

// Канон полосы peer-validate — решение владельца §9 п.1 приёмки XC-6.
// Утверждение стоит ОТДЕЛЬНО от таблицы выше, потому что это не свойство
// реализации, а зафиксированное решение: смена канона обязана уронить именно
// его и назвать себя, а не потеряться строкой в общем переборе.
func TestPeerValidateMissIsFailedPreconditionByOwnerDecision(t *testing.T) {
	require.Equal(t, codes.FailedPrecondition, ReasonPeerResourceMissing.Code(),
		"§9 п.1: промах peer-валидации — FAILED_PRECONDITION (by-lane split)")
}

// Полоса и её носитель обязаны переживать sentinel-слои сервисов: маппер,
// получивший уже собранный статус, отдаёт его как есть. Проверяем то, на что
// сервисы опираются — статус распознаётся как статус, а деталь не теряется.
func TestLaneErrorSurvivesAsStatusForSentinelMappers(t *testing.T) {
	err := ReasonPeerUnavailable.Errf(
		PeerRef{Service: "storage", ResourceType: "geo.zone", ResourceID: "z9"},
		"geo zone validation unavailable")

	st, ok := status.FromError(err)
	require.True(t, ok)
	require.NotEqual(t, codes.Unknown, st.Code(),
		"pass-through сервисных мапперов ключуется на код != Unknown")
	require.Equal(t, codes.Unavailable, st.Code())
	require.Equal(t, "PEER_UNAVAILABLE", infoOf(t, err).GetReason())
}
