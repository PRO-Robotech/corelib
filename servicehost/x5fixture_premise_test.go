// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// x5fixture_premise_test.go — ПРЕДПОСЫЛКА integration-пробы Х5 на дереве ДО
// реализации: фикстура поднимает ПАРУ слушателей через [Serve] и на внутренней
// половине пары даёт ровно те исходы, с которыми проба Х5 сверяет форму
// «только внутренний слушатель».
//
// Порядок несущий: пока эта проба красная, красное пробы формы — сломанный
// вопрос, а не отсутствующая возможность. Её исход читается ПЕРВЫМ.
package servicehost

import (
	"testing"

	"google.golang.org/grpc/codes"

	"github.com/PRO-Robotech/corelib/servicecontract"
)

// TestX5Premise_PairCarrierComesUpAndJudgesTheInternalListener — близнец формы:
// та же фикстура, форма — пара. На внутреннем слушателе:
//
//   - `Ping` (изъятие) без личности доходит до метода — провод и регистрация живы;
//   - `Get` пиром края с личностью администратора: вопросов `Check` 1, вызовов
//     метода 1, `OK`;
//   - `Get` пиром M вне круга с той же личностью: `PERMISSION_DENIED`, текст
//     `"permission denied"`, вопросов `Check` 0, вызовов метода 0;
//   - `Get` без личности пиром края — отказ (код печатается: им сверяется форма).
func TestX5Premise_PairCarrierComesUpAndJudgesTheInternalListener(t *testing.T) {
	f := newX5Fixture(t)
	pub, in := freeAddr(t), freeAddr(t)
	d, err := servicecontract.New(f.pairSpec(pub, in))
	if err != nil {
		t.Fatalf("фикстура: законный дескриптор пары отвергнут — проба Х5 вакуумна: %v", err)
	}
	r := startServe(t, d, noRegistrar(), x5Registrar(f.handled))
	if err := awaitListening(t, r, in); err != nil {
		t.Fatalf("фикстура: носитель пары не поднял внутренний слушатель: %v", err)
	}

	if o := f.callAs(t, in, f.pki.edge, x5Ping, false); o.code != codes.OK || f.handled.ping.Load() != 1 {
		t.Fatalf("фикстура: Ping не дошёл до метода: %v, вызовов %d", o, f.handled.ping.Load())
	}

	if o := f.callAs(t, in, f.pki.edge, x5Get, true); o.code != codes.OK {
		t.Fatalf("фикстура: пир края с личностью администратора получил %v", o)
	}
	if c, h := f.check.calls.Load(), f.handled.get.Load(); c != 1 || h != 1 {
		t.Fatalf("фикстура: пир края — вопросов Check %d, вызовов метода %d; ожидалось 1 и 1", c, h)
	}

	o := f.callAs(t, in, f.pki.outside, x5Get, true)
	if o.code != codes.PermissionDenied || o.msg != "permission denied" {
		t.Fatalf("фикстура: пир вне круга получил %v; ожидался PermissionDenied \"permission denied\"", o)
	}
	if c, h := f.check.calls.Load(), f.handled.get.Load(); c != 1 || h != 1 {
		t.Fatalf("фикстура: пир вне круга дошёл дальше снятия личности — вопросов Check %d, вызовов метода %d "+
			"(ожидалось прежние 1 и 1)", c, h)
	}

	anon := f.callAs(t, in, f.pki.edge, x5Get, false)
	if anon.code == codes.OK {
		t.Fatalf("фикстура: Get без личности дошёл до метода на внутренней половине пары: %v", anon)
	}
	t.Logf("предпосылка: внутренняя половина пары на Get без личности отвечает %v", anon)
}
