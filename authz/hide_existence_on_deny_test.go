// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// hide_existence_on_deny_test.go — отказ пообъектного чтения обязан звучать
// голосом ВЛАДЕЛЬЦА, а не голосом стража.
//
// На один вызов отвечают два независимых решателя: край спрашивает модель по
// записи каталога, сервис-владелец переспрашивает по своей карте. Край на отказе
// пообъектного чтения отдаёт «не найдено» текстом владельца (это и есть скрытие
// существования). Сервис до этой правки отдавал `permission denied` — то есть при
// малейшем расхождении двух решателей вызывающий получал ДРУГОЙ ответ на тот же
// вопрос и по нему отличал «нет доступа» от «нет такого». Различимый ответ и есть
// оракул.
//
// Расхождение не гипотетическое: у края положительный кэш вердиктов со своим
// окном, у сервиса — своё, и в это окно край пропускает вызов, который сервис
// отвергает. Ровно так это и наблюдалось на прогоне — чтение реестра сразу после
// его удаления вернуло 403 там, где соседняя проба той же коллекции требует
// побайтово того же 404, что отдаёт настоящий промах владельца.
package authz_test

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/corelib/authz"
)

// denyAll — вердикт «нет доступа» без sentinel'ов: ровно то, что возвращает
// клиент, не сверяющий существование объекта со своей БД. Именно эта форма
// приводила к 403.
func denyAll(t *testing.T) authz.CheckClient {
	t.Helper()
	return authz.CheckClientFunc(func(context.Context, string, string, string) (bool, error) {
		return false, nil
	})
}

func hideExistenceIntr(t *testing.T, m authz.RPCMap) *authz.Interceptor {
	t.Helper()
	return authz.NewInterceptor(authz.InterceptorOptions{
		ServiceName: "test",
		Map:         m,
		Client:      denyAll(t),
		Cache:       authz.NewCache(0),
	})
}

// TestObjectReadDenyAnswersInTheOwnersVoice — отказ на пообъектном чтении
// приходит как NOT_FOUND с ТЕКСТОМ владельца, побайтово тем же, что даёт
// настоящий промах.
//
// Утверждается СООБЩЕНИЕ, а не только код: различим здесь именно текст, и
// проверка одного кода осталась бы зелёной на «not found» без имени ресурса —
// строке, не похожей ни на один ответ владельца.
func TestObjectReadDenyAnswersInTheOwnersVoice(t *testing.T) {
	cases := []struct {
		name       string
		fullMethod string
		objectType string
		objectID   string
		wantMsg    string
	}{
		{
			name:       "registry",
			fullMethod: "/kacho.cloud.registry.v1.RegistryService/Get",
			objectType: "registry_registry",
			objectID:   "reg0x52qk3qwdrknx8g8",
			wantMsg:    "Registry reg0x52qk3qwdrknx8g8 not found",
		},
		{
			name:       "compute instance",
			fullMethod: "/kacho.cloud.compute.v1.InstanceService/Get",
			objectType: "compute_instance",
			objectID:   "ins00000000000000001",
			wantMsg:    "Instance ins00000000000000001 not found",
		},
		{
			name:       "storage volume",
			fullMethod: "/kacho.cloud.storage.v1.VolumeService/Get",
			objectType: "storage_volume",
			objectID:   "vol00000000000000001",
			wantMsg:    "Volume vol00000000000000001 not found",
		},
		{
			name:       "nlb listener",
			fullMethod: "/kacho.cloud.loadbalancer.v1.ListenerService/Get",
			objectType: "nlb_listener",
			objectID:   "lsn00000000000000001",
			wantMsg:    "Listener lsn00000000000000001 not found",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := authz.RPCMap{tc.fullMethod: {
				Relation: "v_get",
				Extract: authz.StaticExtractor(tc.objectType, func(any) (string, error) {
					return tc.objectID, nil
				}),
			}}
			_, err := runUnary(hideExistenceIntr(t, m), ctxWithPrincipal(t, "usr_x", "user"), tc.fullMethod, &fakeReq{id: tc.objectID})
			st, _ := status.FromError(err)
			if st.Code() != codes.NotFound {
				t.Fatalf("код отказа = %s, ожидался NotFound: пообъектное чтение, отвергнутое стражем, "+
					"отличимо от промаха владельца — это оракул существования", st.Code())
			}
			if st.Message() != tc.wantMsg {
				t.Errorf("текст отказа не совпадает с промахом владельца побайтово:\n  got  = %q\n  want = %q",
					st.Message(), tc.wantMsg)
			}
		})
	}
}

// TestFlaggedMutationDenyAnswersInTheOwnersVoice — то же для мутации, ЯВНО
// объявленной скрывающей существование (registry Update/Delete: край их так и
// помечает в каталоге). Без явного объявления мутация остаётся 403 — см.
// отрицательный контроль ниже.
func TestFlaggedMutationDenyAnswersInTheOwnersVoice(t *testing.T) {
	const fm = "/kacho.cloud.registry.v1.RegistryService/Delete"
	m := authz.RPCMap{fm: {
		Relation:      "v_delete",
		HideExistence: true,
		Extract: authz.StaticExtractor("registry_registry", func(any) (string, error) {
			return "reg0x52qk3qwdrknx8g8", nil
		}),
	}}
	_, err := runUnary(hideExistenceIntr(t, m), ctxWithPrincipal(t, "usr_x", "user"), fm, &fakeReq{})
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound || st.Message() != "Registry reg0x52qk3qwdrknx8g8 not found" {
		t.Fatalf("объявленная скрывающей мутация ответила %s %q, ожидалось NotFound %q",
			st.Code(), st.Message(), "Registry reg0x52qk3qwdrknx8g8 not found")
	}
}

// TestDenyStaysForbiddenWhereThereIsNoExistenceToHide — парный положительный
// контроль отрицания. Без него предыдущие пробы зеленели бы на реализации,
// которая превращает В ЛЮБОЙ отказ в 404, а это стёрло бы разницу между «нет
// прав на действие» и «нет ресурса» там, где скрывать нечего.
func TestDenyStaysForbiddenWhereThereIsNoExistenceToHide(t *testing.T) {
	cases := []struct {
		name       string
		fullMethod string
		entry      authz.RPCEntry
		req        any
		ctx        context.Context
	}{
		{
			// Создание в проекте: объект — сам проект, вызывающий его назвал,
			// существование проекта не скрывается. Отказ обязан остаться отказом.
			name:       "collection scope",
			fullMethod: "/kacho.cloud.registry.v1.RegistryService/Create",
			entry: authz.RPCEntry{Relation: "editor", Extract: authz.StaticExtractor("project", func(any) (string, error) {
				return "prj00000000000000001", nil
			})},
			req: &fakeReq{},
			ctx: ctxWithPrincipal(t, "usr_x", "user"),
		},
		{
			// Мутация БЕЗ явного объявления: край её тоже не скрывает.
			name:       "unflagged mutation",
			fullMethod: "/kacho.cloud.vpc.v1.NetworkService/Update",
			entry: authz.RPCEntry{Relation: "v_update", Extract: authz.StaticExtractor("vpc_network", func(any) (string, error) {
				return "net00000000000000001", nil
			})},
			req: &fakeReq{},
			ctx: ctxWithPrincipal(t, "usr_x", "user"),
		},
		{
			// Тип объекта, у которого нет текста владельца: скопировать нечего,
			// «not found» без имени ресурса сам был бы отличим. Остаётся 403.
			name:       "no owner text",
			fullMethod: "/kacho.cloud.geo.v1.RegionService/Get",
			entry: authz.RPCEntry{Relation: "v_get", Extract: authz.StaticExtractor("cluster", func(any) (string, error) {
				return "cluster_root", nil
			})},
			req: &fakeReq{},
			ctx: ctxWithPrincipal(t, "usr_x", "user"),
		},
		{
			// Идентификатор не назван: подставить в текст владельца нечего, а
			// «Registry  not found» — своя, отличимая, кривая строка.
			name:       "no concrete id",
			fullMethod: "/kacho.cloud.registry.v1.RegistryService/Get",
			entry: authz.RPCEntry{Relation: "v_get", Extract: authz.StaticExtractor("registry_registry", func(any) (string, error) {
				return "", nil
			})},
			req: &fakeReq{},
			ctx: ctxWithPrincipal(t, "usr_x", "user"),
		},
		{
			// Никто не назвался. Это НЕ про существование ресурса, и ответ
			// «не найдено» здесь соврал бы неаутентифицированному о наличии прав.
			name:       "no principal",
			fullMethod: "/kacho.cloud.registry.v1.RegistryService/Get",
			entry: authz.RPCEntry{Relation: "v_get", Extract: authz.StaticExtractor("registry_registry", func(any) (string, error) {
				return "reg0x52qk3qwdrknx8g8", nil
			})},
			req: &fakeReq{},
			ctx: context.Background(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := authz.RPCMap{tc.fullMethod: tc.entry}
			_, err := runUnary(hideExistenceIntr(t, m), tc.ctx, tc.fullMethod, tc.req)
			st, _ := status.FromError(err)
			if st.Code() != codes.PermissionDenied {
				t.Fatalf("код = %s, ожидался PermissionDenied: скрывать здесь нечего, "+
					"а 404 стёр бы разницу между «нет прав» и «нет ресурса»", st.Code())
			}
			if strings.Contains(strings.ToLower(st.Message()), "not found") {
				t.Errorf("сообщение %q говорит об отсутствии ресурса там, где решение было об отказе", st.Message())
			}
		})
	}
}
