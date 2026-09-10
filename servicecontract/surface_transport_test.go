// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// surface_transport_test.go — принятое объявление отвечает, под транспортом ли
// провод поверхности.
//
// ПРЕДМЕТ. Самоотчёт процесса о посадке (pkg/observability) обязан называть
// состояние собственных REST-фронтов, и величина обязана ВЫВОДИТЬСЯ из того же
// объявления, по которому поверхность поднимается. Иначе самоотчёт и доклад
// поверхности при подъёме — два утверждения об одном предмете, расходящиеся
// молча.
package servicecontract_test

import (
	"crypto/tls"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/servicecontract"
)

// transportProbeSurface — законное объявление, у которого проба меняет РОВНО
// ОДИН факт: адрес либо транспорт.
func transportProbeSurface(addr servicecontract.Axis[string], tlsCfg *tls.Config) servicecontract.Surface {
	return servicecontract.Surface{
		Service:          "kaname",
		Name:             "собственный публичный REST-фронт",
		Mode:             servicecontract.ModeProduction,
		Logger:           slog.Default(),
		Addr:             addr,
		Handler:          http.NewServeMux(),
		Reach:            servicecontract.ReachExternal,
		Auth:             servicecontract.Value[servicecontract.SurfaceAuthMech]("предъявленное арендатором удостоверение"),
		TLS:              tlsCfg,
		ReadHeaderBudget: 10 * time.Second,
		RequestBudget:    servicecontract.Value(30 * time.Second),
		IdleBudget:       90 * time.Second,
		ShutdownBudget:   5 * time.Second,
	}
}

// TestSurfaceDescriptor_UnderTLSAnswersAboutTheWire — принятое объявление
// отвечает про ПРОВОД, и ответ не зависит от того, поднимается ли поверхность.
func TestSurfaceDescriptor_UnderTLSAnswersAboutTheWire(t *testing.T) {
	raised := servicecontract.Value("127.0.0.1:9098")
	notRaised := servicecontract.NotApplicable[string](
		"KANAME_API_SERVER__REST_ENDPOINT не задан профилем развёртывания: собственной " +
			"HTTP-поверхности у службы на этой посадке нет")

	for _, tc := range []struct {
		name         string
		addr         servicecontract.Axis[string]
		tlsCfg       *tls.Config
		wantEnabled  bool
		wantUnderTLS bool
	}{
		{"поднят под транспортом", raised, &tls.Config{MinVersion: tls.VersionTLS13}, true, true},
		{"поднят открытым текстом", raised, nil, true, false},
		{"не поднят", notRaised, nil, false, false},
		// Транспорт объявлен, а поверхность не поднимается. Ответы РАЗНЫЕ по
		// построению: один про поверхность, другой про провод, и слить их
		// значило бы отчитываться о защите того, чего нет.
		{"не поднят, транспорт объявлен", notRaised, &tls.Config{MinVersion: tls.VersionTLS13}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := servicecontract.NewSurface(transportProbeSurface(tc.addr, tc.tlsCfg))
			if err != nil {
				t.Fatalf("законное объявление отвергнуто конструктором: %v", err)
			}
			if got := d.Enabled(); got != tc.wantEnabled {
				t.Fatalf("Enabled() = %v, ждали %v", got, tc.wantEnabled)
			}
			if got := d.UnderTLS(); got != tc.wantUnderTLS {
				t.Fatalf("UnderTLS() = %v, ждали %v", got, tc.wantUnderTLS)
			}
		})
	}
}

// TestSurfaceDescriptor_UnderTLSIsFalseOnAnUnacceptedProfile — нулевое значение
// НЕМЫМ быть не вправе: профиль, собранный литералом, не проходил ни одного
// отказа, и «под транспортом» о нём утверждать нечего.
//
// Ответ строгий (ложь), а не «не знаю»: забыть здесь можно только в сторону
// отказа гейта, а не в сторону его молчания.
func TestSurfaceDescriptor_UnderTLSIsFalseOnAnUnacceptedProfile(t *testing.T) {
	var zero servicecontract.SurfaceDescriptor
	if zero.Accepted() {
		t.Fatal("нулевой профиль объявил себя принятым — предпосылка пробы неверна")
	}
	if zero.UnderTLS() {
		t.Fatal("непринятый профиль объявил провод защищённым: самоотчёт получил бы " +
			"утверждение о транспорте, которого никто не принимал")
	}
}
