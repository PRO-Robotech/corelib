// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package authz_test

// decision_lane_codes_test.go — три полосы отказа звена решения о доступе отвечают
// ТРЕМЯ разными кодами.
//
// Полосы различаются не строгостью, а тем, ЧТО произошло, и вызывающий читает
// именно код: по нему он решает, осмыслен ли повтор.
//
//   - модель не ответила        → UNAVAILABLE       («спроси ещё раз»)
//   - модель ответила отказом   → PERMISSION_DENIED («тебе нельзя», повтор бесполезен)
//   - модель скрывает объект    → NOT_FOUND          (голосом владельца, побайтно)
//
// Fail-closed у всех трёх одинаков и под сомнение здесь не ставится: обработчик не
// вызывается ни в одной. Проба утверждает ПАРУ «код + не вызвали обработчик», а не
// одно из двух, — иначе схлопывание полос в один код осталось бы зелёным.
//
// Почему это отдельная проба, а не строка в соседних. Полосы разведены по трём
// файлам (`interceptor_test.go`, `hide_existence_test.go`), и каждая утверждает
// свой код в одиночку. Схлопывание двух полос в один код при этом остаётся
// зелёным у обеих: каждая видит СВОЙ ответ и ничего не знает про соседний. Здесь
// они сверяются друг с другом в одном прогоне — попарная различимость и есть
// предмет.
//
// Полоса недоступности отвечала кодом отказа в правах, и следствие наблюдалось на
// стенде: у потребителя ветка разбора недоступности не срабатывала НИКОГДА, потому
// что этот код до неё не доходил, а переходный сбой становился терминальным
// отказом операции. Задача #497.
//
// # Почему полос две ПО RPC, а не одна на всех
//
// Первая редакция этой пробы спрашивала все три полосы на одном RPC — и упала на
// том, что дефектом не является: пообъектное чтение (`/Get` на глагольном `v_get`)
// отвечает на ОТКАЗ голосом владельца, а не `PermissionDenied`
// (`HidesExistenceOnDeny`, hide_existence.go). То есть на скрывающем RPC отказ и
// скрытие — одна полоса by design, и требовать от них разных кодов значило бы
// требовать оракула существования. Поэтому отказ спрашивается у НЕскрывающего RPC,
// скрытие — у скрывающего, а недоступность — у ОБОИХ: она не про объект и обязана
// звучать одинаково независимо от того, скрывает ли RPC.

import (
	"context"
	"errors"
	"testing"

	"github.com/PRO-Robotech/corelib/authz"
	"github.com/PRO-Robotech/corelib/operations"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// laneGetRPC — пообъектное чтение: `/Get` на глагольном `v_get` с типом,
	// у которого в таблице владельцев есть текст. Только такая форма доходит до
	// полосы скрытия.
	laneGetRPC = "/kacho.cloud.vpc.v1.NetworkService/Get"
	// laneCreateRPC — мутация: скрытия не производит, поэтому её отказ звучит
	// отказом.
	laneCreateRPC = "/kacho.cloud.vpc.v1.NetworkService/Create"

	laneNetworkID = "enp7tp1q22pfqey44m4m"
	laneProjectID = "prj7tp1q22pfqey44m4m"
)

func laneMap() authz.RPCMap {
	return authz.RPCMap{
		laneGetRPC: {
			Relation: "v_get",
			Extract: authz.StaticExtractor("vpc_network", func(req any) (string, error) {
				return req.(*fakeReq).id, nil
			}),
		},
		laneCreateRPC: {
			Relation: "v_create",
			Extract: authz.StaticExtractor("project", func(req any) (string, error) {
				return req.(*fakeReq).id, nil
			}),
		},
	}
}

func laneInterceptor(t *testing.T, answer func() (bool, error)) *authz.Interceptor {
	t.Helper()
	return authz.NewInterceptor(authz.InterceptorOptions{
		Cache:       authz.NewCache(0),
		ServiceName: "kacho-lane-test",
		Map:         laneMap(),
		Client: authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
			return answer()
		}),
	})
}

// runLane прогоняет unary-звено и возвращает (вызвали ли обработчик, ошибку).
// Факт вызова — половина утверждения: код без него не отличает «отказали» от
// «пропустили, а обработчик ошибся сам».
func runLane(intr *authz.Interceptor, rpc, objectID string) (bool, error) {
	called := false
	handler := func(ctx context.Context, req any) (any, error) {
		called = true
		return "handled", nil
	}
	ctx := operations.WithPrincipal(context.Background(), operations.Principal{
		Type: "user", ID: "usr_alice", DisplayName: "usr_alice",
	})
	_, err := intr.Unary()(ctx, &fakeReq{id: objectID},
		&grpc.UnaryServerInfo{FullMethod: rpc}, handler)
	return called, err
}

// ответы модели прав — ровно те, что производят полосы.
func modelSilent() (bool, error)  { return false, errors.New("context deadline exceeded") }
func modelRefuses() (bool, error) { return false, nil }
func modelHides() (bool, error)   { return false, authz.ErrHideExistence }

// TestDecisionLanes_ThreeLanesThreeCodes — предмет пробы.
func TestDecisionLanes_ThreeLanesThreeCodes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rpc      string
		objectID string
		answer   func() (bool, error)
		want     codes.Code
		msg      string
		why      string
	}{
		{
			name: "модель не ответила (мутация)", rpc: laneCreateRPC, objectID: laneProjectID,
			answer: modelSilent, want: codes.Unavailable, msg: "authorization service unavailable",
			why: "недоступность модели — не решение о правах; вызывающий обязан прочесть её как " +
				"повторяемую, иначе перебой на доли секунды убивает операцию",
		},
		{
			name: "модель не ответила (скрывающее чтение)", rpc: laneGetRPC, objectID: laneNetworkID,
			answer: modelSilent, want: codes.Unavailable, msg: "authorization service unavailable",
			why: "недоступность не про объект: она звучит одинаково и там, где RPC скрывает " +
				"существование, — оракула этим не заводится",
		},
		{
			name: "модель ответила отказом", rpc: laneCreateRPC, objectID: laneProjectID,
			answer: modelRefuses, want: codes.PermissionDenied, msg: "permission denied",
			why: "решение модели терминально: повтор идентичного запроса его не изменит",
		},
		{
			name: "модель скрывает существование", rpc: laneGetRPC, objectID: laneNetworkID,
			answer: modelHides, want: codes.NotFound, msg: "Network " + laneNetworkID + " not found",
			why: "скрытие говорит голосом владельца побайтно, иначе это оракул существования",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called, err := runLane(laneInterceptor(t, tc.answer), tc.rpc, tc.objectID)

			if called {
				t.Fatalf("fail-closed нарушен: обработчик вызван на полосе %q", tc.name)
			}
			if err == nil {
				t.Fatalf("полоса %q обязана отказать", tc.name)
			}
			if got := status.Code(err); got != tc.want {
				t.Fatalf("полоса %q: код %v, ожидался %v — %s", tc.name, got, tc.want, tc.why)
			}
			if got := status.Convert(err).Message(); got != tc.msg {
				t.Fatalf("полоса %q: текст %q, ожидался %q", tc.name, got, tc.msg)
			}
		})
	}
}

// TestDecisionLanes_CodesArePairwiseDistinct — попарная различимость как отдельное
// утверждение о НАБОРЕ.
//
// Без неё правка, схлопнувшая две полосы в один код, прошла бы проверку выше ровно
// в тот день, когда кто-нибудь заодно поправит ожидание.
//
// Сверяются полосы, которые обязаны различаться у ОДНОГО вызывающего: на мутации —
// недоступность против отказа; скрытие сверяется отдельно, потому что оно живёт
// только на читающем RPC.
func TestDecisionLanes_CodesArePairwiseDistinct(t *testing.T) {
	answers := map[string]func() (bool, error){
		"недоступность": modelSilent,
		"отказ":         modelRefuses,
	}

	seen := make(map[codes.Code]string, len(answers))
	for lane, answer := range answers {
		_, err := runLane(laneInterceptor(t, answer), laneCreateRPC, laneProjectID)
		code := status.Code(err)
		if other, dup := seen[code]; dup {
			t.Fatalf("полосы %q и %q отвечают одним кодом %v — вызывающий их не различит",
				lane, other, code)
		}
		seen[code] = lane
	}

	// Скрытие — третий код, и он не совпадает ни с одним из двух.
	_, hideErr := runLane(laneInterceptor(t, modelHides), laneGetRPC, laneNetworkID)
	hideCode := status.Code(hideErr)
	if other, dup := seen[hideCode]; dup {
		t.Fatalf("полоса скрытия отвечает тем же кодом %v, что и %q", hideCode, other)
	}
	seen[hideCode] = "скрытие"

	if len(seen) != 3 {
		t.Fatalf("осмотрено полос 3, различимых кодов %d", len(seen))
	}
}

// TestDecisionLanes_AllowedStillRunsTheHandler — положительный контроль пары.
//
// Без него все утверждения выше зеленели бы на звене, которое отказывает ВСЕМУ:
// «обработчик не вызван» — свойство, которое сломанное звено выполняет идеально.
func TestDecisionLanes_AllowedStillRunsTheHandler(t *testing.T) {
	for _, rpc := range []struct {
		name     string
		method   string
		objectID string
	}{
		{"скрывающее чтение", laneGetRPC, laneNetworkID},
		{"мутация", laneCreateRPC, laneProjectID},
	} {
		t.Run(rpc.name, func(t *testing.T) {
			allow := func() (bool, error) { return true, nil }
			called, err := runLane(laneInterceptor(t, allow), rpc.method, rpc.objectID)
			if err != nil {
				t.Fatalf("разрешённый вызов обязан пройти, получено: %v", err)
			}
			if !called {
				t.Fatalf("разрешённый вызов обязан дойти до обработчика")
			}
		})
	}
}
