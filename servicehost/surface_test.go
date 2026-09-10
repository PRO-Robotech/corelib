// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// surface_test.go — пробы на ПОВЕДЕНИЕ профиля не-gRPC поверхности.
//
// Утверждается наблюдаемое, а не форма вызова: порт занят, пока поверхность
// живёт, и СВОБОДЕН после возврата функции. Проба вида «Shutdown был вызван»
// зеленела бы на слушателе, который порт всё ещё держит, — то есть ровно на том
// дефекте, ради которого профиль написан.
package servicehost_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/PRO-Robotech/corelib/servicecontract"
	"github.com/PRO-Robotech/corelib/servicehost"
)

// freeAddr занимает и тут же отпускает порт, чтобы получить адрес, который
// заведомо свободен ПРЯМО СЕЙЧАС.
//
// Фиксированный адрес нужен по существу: доказательство «порт освобождён»
// строится повторной привязкой к ТОМУ ЖЕ адресу, а слушатель на `:0` каждый раз
// брал бы новый порт и проба утверждала бы про свободу произвольного порта.
func freeAddr(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("занять порт под пробу: %v", err)
	}
	addr := lis.Addr().String()
	if cerr := lis.Close(); cerr != nil {
		t.Fatalf("отпустить порт под пробу: %v", cerr)
	}
	return addr
}

func probeSurface(t *testing.T, addr string) servicecontract.Surface {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "kacho_probe 1\n")
	})
	return servicecontract.Surface{
		Service: "kacho-demo",
		Name:    "метрики",
		Mode:    servicecontract.ModeProduction,
		Addr:    servicecontract.Value(addr),
		Handler: mux,
		Reach:   servicecontract.ReachClusterInternal,
		Auth: servicecontract.NotApplicable[servicecontract.SurfaceAuthMech](
			"внутренний скрейп, на проводе только счётчики процесса"),
		ReadHeaderBudget: 2 * time.Second,
		RequestBudget:    servicecontract.Value(5 * time.Second),
		IdleBudget:       10 * time.Second,
		ShutdownBudget:   3 * time.Second,
	}
}

// TestSurfaceReleasesItsPortBeforeReturning — ЦЕНТРАЛЬНАЯ проба профиля.
//
// Слушатель, переживший отмену контекста, держит порт и мешает следующему
// старту того же процесса. Здесь это проверяется единственным способом, который
// нельзя подделать вызовом гашения: после возврата функции к ТОМУ ЖЕ адресу
// удаётся привязаться заново.
func TestSurfaceReleasesItsPortBeforeReturning(t *testing.T) {
	addr := freeAddr(t)
	d, err := servicecontract.NewSurface(probeSurface(t, addr))
	if err != nil {
		t.Fatalf("законный профиль отвергнут: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	wait, serr := servicehost.ServeSurface(ctx, d)
	if serr != nil {
		t.Fatalf("законный профиль не поднялся: %v", serr)
	}
	done := make(chan error, 1)
	go func() { done <- wait() }()

	// Положительный контроль: пока контекст жив, поверхность ОТВЕЧАЕТ. Без него
	// «порт свободен» ниже было бы верно и для поверхности, которая не поднялась
	// вовсе, — то есть отрицание зеленело бы на полностью сломанном профиле.
	body := getUntil(t, "http://"+addr+"/metrics", 3*time.Second)
	if !strings.Contains(body, "kacho_probe") {
		t.Fatalf("поверхность поднята, но отдала не своё тело: %q", body)
	}

	cancel()
	select {
	case serr := <-done:
		if serr != nil {
			t.Fatalf("гашение по отмене контекста вернуло ошибку: %v", serr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("функция не вернулась после отмены контекста — слушатель пережил свой контекст")
	}

	lis, berr := net.Listen("tcp", addr)
	if berr != nil {
		t.Fatalf("порт %s занят ПОСЛЕ возврата функции — следующий старт того же процесса "+
			"споткнётся о собственный предыдущий: %v", addr, berr)
	}
	_ = lis.Close()
}

// TestSurfaceDisabledByDeclarationBindsNothing — выключение объявлением есть
// исход, а не пропуск: функция возвращается сразу, и порта не занимает.
func TestSurfaceDisabledByDeclarationBindsNothing(t *testing.T) {
	addr := freeAddr(t)
	s := probeSurface(t, addr)
	s.Addr = servicecontract.NotApplicable[string](
		"на этой посадке скрейпа нет: эндпоинт диагностики профилем не задан")
	s.Handler = nil
	d, err := servicecontract.NewSurface(s)
	if err != nil {
		t.Fatalf("объявленное выключение отвергнуто: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wait, serr := servicehost.ServeSurface(ctx, d)
	if serr != nil {
		t.Fatalf("выключенная поверхность отказала при подъёме: %v", serr)
	}
	done := make(chan error, 1)
	go func() { done <- wait() }()
	select {
	case serr := <-done:
		if serr != nil {
			t.Fatalf("выключенная поверхность вернула ошибку: %v", serr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("выключенная поверхность не вернулась — значит что-то всё-таки поднялось")
	}

	lis, berr := net.Listen("tcp", addr)
	if berr != nil {
		t.Fatalf("поверхность объявлена выключенной, а порт %s занят: %v", addr, berr)
	}
	_ = lis.Close()
}

// TestSurfaceRefusesUnacceptedProfile — профиль, собранный литералом, не
// проходил ни одного отказа. Поднимать по нему слушатель значит не проверить
// ничего, и это отказ, а не умолчание.
func TestSurfaceRefusesUnacceptedProfile(t *testing.T) {
	wait, err := servicehost.ServeSurface(context.Background(), servicecontract.SurfaceDescriptor{})
	if wait != nil {
		t.Fatal("непринятый профиль вернул ожидание — вызывающему есть что позвать " +
			"после отказа, и он позовёт")
	}
	if err == nil {
		t.Fatal("непринятый профиль поднят — конструктор перестал быть обязательным")
	}
	if !strings.Contains(err.Error(), "не принят") {
		t.Fatalf("отказ не называет предмет: %v", err)
	}
}

// TestSurfaceReportsBindFailureInsteadOfLoggingIt — занятый адрес есть ОТКАЗ,
// а не строка в журнале.
//
// Проба заведена потому, что этот дефект в дереве был: плоскость данных реестра
// поднималась `ListenAndServe` в горутине, и занятый порт попадал в журнал —
// процесс продолжал работать, объявляя себя исправным, при мёртвой поверхности.
func TestSurfaceReportsBindFailureInsteadOfLoggingIt(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("занять порт под пробу: %v", err)
	}
	defer func() { _ = held.Close() }()

	d, derr := servicecontract.NewSurface(probeSurface(t, held.Addr().String()))
	if derr != nil {
		t.Fatalf("законный профиль отвергнут: %v", derr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wait, serr := servicehost.ServeSurface(ctx, d)
	if serr == nil {
		t.Fatal("привязка к занятому адресу прошла молча — вызывающий не узнает, " +
			"что поверхности нет")
	}
	if !strings.Contains(serr.Error(), held.Addr().String()) {
		t.Fatalf("отказ не называет адрес — по нему нечего чинить: %v", serr)
	}
	if wait != nil {
		t.Fatal("отказ привязки вернул ожидание: вызывающий, написавший `wait, err := …` " +
			"и позвавший wait() в задаче супервизора, повис бы вместо отказа")
	}
}

// Отказ привязки приходит ДО обслуживания, а не кодом возврата процесса.
//
// Дефект, ради которого проба заведена, был живым и на трёх сервисах сразу:
// подъём поверхности уезжал в горутину, исход читался на выходе — то есть
// занятый порт становился кодом возврата процесса, успевшего сколько угодно
// проработать при мёртвой поверхности. Верное использование зависело от
// дисциплины вызывающего; теперь оно следует из сигнатуры, и проба это
// закрепляет.
func TestSurfaceBindFailureArrivesBeforeAnyServing(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("занять порт под пробу: %v", err)
	}
	defer func() { _ = held.Close() }()

	d, derr := servicecontract.NewSurface(probeSurface(t, held.Addr().String()))
	if derr != nil {
		t.Fatalf("законный профиль отвергнут: %v", derr)
	}
	// Контекст НЕ отменяется: отказ обязан прийти сам, а не по отмене. Проба на
	// живом контексте отличает «привязка не удалась» от «мы дождались гашения».
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	returned := make(chan error, 1)
	go func() {
		_, berr := servicehost.ServeSurface(ctx, d)
		returned <- berr
	}()
	select {
	case berr := <-returned:
		if berr == nil {
			t.Fatal("привязка к занятому адресу объявлена успешной")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("отказ привязки не пришёл на живом контексте — значит подъём снова ждёт " +
			"отмены, и процесс объявит себя поднявшимся при мёртвой поверхности")
	}

	// Законный близнец: на СВОБОДНОМ адресе тот же вызов возвращается успехом и
	// тоже сразу — иначе утверждение выше зеленело бы на функции, которая всегда
	// отказывает.
	free, ferr := servicecontract.NewSurface(probeSurface(t, freeAddr(t)))
	if ferr != nil {
		t.Fatalf("законный профиль отвергнут: %v", ferr)
	}
	wait, serr := servicehost.ServeSurface(ctx, free)
	if serr != nil {
		t.Fatalf("свободный адрес отвергнут при подъёме: %v", serr)
	}
	cancel()
	if werr := wait(); werr != nil {
		t.Fatalf("гашение вернуло ошибку: %v", werr)
	}
	// Ожидание идемпотентно: второй вопрос отвечает тем же, а не блокирует.
	if werr := wait(); werr != nil {
		t.Fatalf("повторное ожидание ответило иначе: %v", werr)
	}
}

// getUntil ждёт, пока поверхность начнёт отвечать. Ожидание нужно потому, что
// привязка порта и первый принятый запрос — разные моменты; фиксированная пауза
// вместо ожидания сделала бы пробу флейкой на загруженной машине.
func getUntil(t *testing.T, url string, budget time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(budget)
	var last error
	for time.Now().Before(deadline) {
		resp, err := http.Get(url) //nolint:noctx // проба, срок держит deadline петли
		if err != nil {
			last = err
			time.Sleep(20 * time.Millisecond)
			continue
		}
		body, rerr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if rerr != nil {
			last = rerr
			continue
		}
		return string(body)
	}
	t.Fatalf("поверхность не ответила за %s: %v", budget, errors.Join(last, fmt.Errorf("url %s", url)))
	return ""
}

// TestSurfaceDeclaredTLSReachesTheListener — объявленный транспорт ДОХОДИТ до
// слушателя, и это утверждается наблюдаемым.
//
// Проба заведена вместо трёх текстовых, живших в композиционном корне владельца
// модели: те искали в исходнике строку обёртки слушателя и остались бы зелёными,
// напиши кто-нибудь эту строку в комментарии, — и покраснели бы, переедь обёртка
// на этаж ниже (что и произошло). Здесь предмет тот же, но утверждается он на
// проводе: открытым текстом до обработчика не достучаться, по TLS — штатный ответ.
func TestSurfaceDeclaredTLSReachesTheListener(t *testing.T) {
	addr := freeAddr(t)
	certPEM, tlsCfg := selfSignedTLS(t)

	s := probeSurface(t, addr)
	s.Name = "поверхность под объявленным транспортом"
	s.TLS = tlsCfg
	d, err := servicecontract.NewSurface(s)
	if err != nil {
		t.Fatalf("законный профиль отвергнут: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	wait, serr := servicehost.ServeSurface(ctx, d)
	if serr != nil {
		t.Fatalf("профиль с объявленным транспортом не поднялся: %v", serr)
	}
	done := make(chan error, 1)
	go func() { done <- wait() }()
	defer func() {
		cancel()
		<-done
	}()

	// (б) положительный контроль ПЕРВЫМ: по TLS с доверием к тому же корню
	// обработчик отвечает своим телом. Без него отрицание ниже зеленело бы на
	// поверхности, которая не поднялась вовсе.
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		t.Fatal("корень пробы не разбирается")
	}
	secure := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
	}
	var body string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, gerr := secure.Get("https://" + addr + "/metrics") //nolint:noctx // срок держит клиент
		if gerr != nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		body = string(b)
		break
	}
	if !strings.Contains(body, "kacho_probe") {
		t.Fatalf("по объявленному транспорту обработчик не ответил своим телом: %q", body)
	}

	// (а) открытым текстом обработчик недостижим.
	plain := &http.Client{Timeout: 3 * time.Second}
	resp, gerr := plain.Get("http://" + addr + "/metrics") //nolint:noctx // срок держит клиент
	if gerr == nil {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if strings.Contains(string(b), "kacho_probe") {
			t.Fatal("открытым текстом запрос ДОШЁЛ до обработчика — объявленный транспорт до " +
				"слушателя не доехал, и объявление о нём было бы ложью")
		}
	}
}

// selfSignedTLS выдаёт самоподписанный корень и готовый транспорт слушателя.
func selfSignedTLS(t *testing.T) (certPEM []byte, cfg *tls.Config) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("ключ пробы: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "kacho-surface-probe"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("сертификат пробы: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("пара пробы: %v", err)
	}
	return certPEM, &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
}
