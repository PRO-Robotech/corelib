// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcsrv

// admission_test.go — допуск утверждается на ИСХОДЕ вызова, а не на состоянии
// ведра.
//
// Каждое отрицание («сверх предела — отказ») стоит В ПАРЕ с положительным
// контролем («под пределом — проходит»): ограничитель, отвергающий всё, зеленит
// любое отрицание, и отличить его от исправного нечем.
//
// Часы подменены везде, где предмет случая — время. Проба, ждущая настоящую
// секунду, либо медленная, либо недетерминированная, а недетерминизм входа гейт
// однажды прочтёт как свойство предмета.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/corelib/operations"
)

// probeLimits — небольшие величины: предмет случаев — поведение на границе, а не
// числа посадки (их закрепляет проба конфигурации).
func probeLimits() AdmissionLimits {
	return AdmissionLimits{ReadPerSec: 10, MutationPerSec: 4, BurstFactor: 2, InFlight: 3}
}

// tenantCtx — контекст с личностью конечного пользователя.
func tenantCtx(id string) context.Context {
	return operations.WithPrincipal(context.Background(),
		operations.Principal{Type: "user", ID: id})
}

// frozenClock — управляемые часы.
type frozenClock struct {
	mu sync.Mutex
	at time.Time
}

func newFrozenClock() *frozenClock {
	return &frozenClock{at: time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)}
}

func (c *frozenClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *frozenClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

const (
	probeRead     = "/kacho.cloud.vpc.v1.NetworkService/List"
	probeMutation = "/kacho.cloud.vpc.v1.NetworkService/Create"
)

// admitN зовёт допуск n раз, сразу освобождая слот, и отдаёт первую ошибку.
func admitN(t *testing.T, a *Admission, ctx context.Context, method string, n int) error {
	t.Helper()
	for i := 0; i < n; i++ {
		release, err := a.Admit(ctx, method)
		if err != nil {
			return err
		}
		release()
	}
	return nil
}

// TestAdmissionAdmitsUpToTheBurst — ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ ко всем отрицаниям
// ниже: законный поток проходит целиком.
func TestAdmissionAdmitsUpToTheBurst(t *testing.T) {
	clock := newFrozenClock()
	a, err := NewAdmission("public", probeLimits(), PrincipalSubject, WithAdmissionClock(clock.now))
	require.NoError(t, err)

	// Всплеск = темп × кратность = 10 × 2 = 20 чтений на замороженных часах.
	require.NoError(t, admitN(t, a, tenantCtx("usr-1"), probeRead, 20),
		"весь объявленный всплеск обязан проходить: ограничитель, отвергающий законный поток, "+
			"зеленит любое отрицание и неотличим от исправного")
	require.Equal(t, uint64(20), a.Stats().Admitted)
	require.Zero(t, a.Stats().RejectedRate)
}

// TestAdmissionRefusesOverTheReadRate — сверх всплеска чтений → отказ с кодом и
// текстом контракта.
func TestAdmissionRefusesOverTheReadRate(t *testing.T) {
	clock := newFrozenClock()
	a, err := NewAdmission("public", probeLimits(), PrincipalSubject, WithAdmissionClock(clock.now))
	require.NoError(t, err)

	require.NoError(t, admitN(t, a, tenantCtx("usr-1"), probeRead, 20))

	_, err = a.Admit(tenantCtx("usr-1"), probeRead)
	require.Error(t, err, "запрос сверх всплеска обязан быть отвергнут")
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.ResourceExhausted, st.Code(),
		"код обязан быть RESOURCE_EXHAUSTED (край отобразит его в 429), а не UNAVAILABLE: "+
			"исход детерминирован вводом, а не сбоем")
	require.Equal(t, MsgReadRateExceeded, st.Message(),
		"текст отказа обязан называть ПРЕДМЕТ — что именно исчерпано")
	require.NotEmpty(t, st.Details(),
		"отказ обязан нести RetryInfo: без «когда» повтор превращается в ту же нагрузку")
	require.Equal(t, uint64(1), a.Stats().RejectedRate)
}

// TestAdmissionRefusesOverTheMutationRate — у мутаций СВОЙ бюджет и свой текст.
func TestAdmissionRefusesOverTheMutationRate(t *testing.T) {
	clock := newFrozenClock()
	a, err := NewAdmission("public", probeLimits(), PrincipalSubject, WithAdmissionClock(clock.now))
	require.NoError(t, err)

	// Всплеск мутаций = 4 × 2 = 8.
	require.NoError(t, admitN(t, a, tenantCtx("usr-1"), probeMutation, 8))

	_, err = a.Admit(tenantCtx("usr-1"), probeMutation)
	require.Error(t, err)
	require.Equal(t, MsgMutationRateExceeded, status.Convert(err).Message())
}

// TestAdmissionKeepsReadsAndMutationsApart — исчерпание одного класса НЕ
// закрывает другой.
//
// Это и есть смысл двух объявленных величин: у чтения и мутации разная
// стоимость, и арендатор, исчерпавший запись, обязан по-прежнему читать своё.
func TestAdmissionKeepsReadsAndMutationsApart(t *testing.T) {
	clock := newFrozenClock()
	a, err := NewAdmission("public", probeLimits(), PrincipalSubject, WithAdmissionClock(clock.now))
	require.NoError(t, err)

	require.NoError(t, admitN(t, a, tenantCtx("usr-1"), probeMutation, 8))
	_, err = a.Admit(tenantCtx("usr-1"), probeMutation)
	require.Error(t, err, "бюджет мутаций обязан быть исчерпан")

	require.NoError(t, admitN(t, a, tenantCtx("usr-1"), probeRead, 20),
		"исчерпанные мутации не должны закрывать чтения — это разные величины")
}

// TestAdmissionKeepsSubjectsApart — предел НА АРЕНДАТОРА, а не на процесс.
//
// Без этого случая ограничитель с общим ведром выглядел бы исправным на всех
// предыдущих: они спрашивают одного субъекта.
func TestAdmissionKeepsSubjectsApart(t *testing.T) {
	clock := newFrozenClock()
	a, err := NewAdmission("public", probeLimits(), PrincipalSubject, WithAdmissionClock(clock.now))
	require.NoError(t, err)

	require.NoError(t, admitN(t, a, tenantCtx("usr-1"), probeRead, 20))
	_, err = a.Admit(tenantCtx("usr-1"), probeRead)
	require.Error(t, err)

	require.NoError(t, admitN(t, a, tenantCtx("usr-2"), probeRead, 20),
		"исчерпание одного арендатора не должно касаться другого")
}

// TestAdmissionRefillsOverTime — ведро наполняется по прошедшему времени.
func TestAdmissionRefillsOverTime(t *testing.T) {
	clock := newFrozenClock()
	a, err := NewAdmission("public", probeLimits(), PrincipalSubject, WithAdmissionClock(clock.now))
	require.NoError(t, err)

	require.NoError(t, admitN(t, a, tenantCtx("usr-1"), probeRead, 20))
	_, err = a.Admit(tenantCtx("usr-1"), probeRead)
	require.Error(t, err, "предпосылка случая: бюджет исчерпан")

	clock.advance(time.Second) // 10 чтений в секунду
	require.NoError(t, admitN(t, a, tenantCtx("usr-1"), probeRead, 10),
		"за секунду обязано вернуться ровно устойчивое число токенов")
	_, err = a.Admit(tenantCtx("usr-1"), probeRead)
	require.Error(t, err, "и ни одним больше: пополнение не должно превращать пол в потолок")
}

// TestAdmissionRefusesOverTheInFlightCap — предел ОДНОВРЕМЕННЫХ запросов
// исполняется отдельно от темпа.
//
// Слоты не освобождаются, поэтому темп здесь заведомо не исчерпан (3 запроса
// против всплеска в 20): случай утверждает именно одновременность.
func TestAdmissionRefusesOverTheInFlightCap(t *testing.T) {
	clock := newFrozenClock()
	a, err := NewAdmission("public", probeLimits(), PrincipalSubject, WithAdmissionClock(clock.now))
	require.NoError(t, err)

	var releases []func()
	for i := 0; i < 3; i++ {
		release, err := a.Admit(tenantCtx("usr-1"), probeRead)
		require.NoError(t, err)
		releases = append(releases, release)
	}

	_, err = a.Admit(tenantCtx("usr-1"), probeRead)
	require.Error(t, err, "запрос сверх предела одновременности обязан быть отвергнут")
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
	require.Equal(t, MsgInFlightExceeded, status.Convert(err).Message(),
		"текст обязан отличать одновременность от темпа: арендатор, упёршийся в одну, "+
			"ведёт себя иначе, чем упёршийся в другую")
	require.Equal(t, uint64(1), a.Stats().RejectedInFlight)
	require.Zero(t, a.Stats().RejectedRate, "по темпу отказа быть не должно — он не исчерпан")

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: освободившийся слот снова допускает.
	releases[0]()
	release, err := a.Admit(tenantCtx("usr-1"), probeRead)
	require.NoError(t, err, "освобождённый слот обязан снова допускать — иначе предел «съедает» ёмкость навсегда")
	release()
	for _, r := range releases[1:] {
		r()
	}
}

// TestAdmissionReleaseIsIdempotent — двойное освобождение не возвращает лишний
// слот.
//
// Предмет не гипотетический: обёртка дескриптора зовёт освобождение через
// `defer`, а обработчик может вернуться и по панике, перехваченной звеном выше.
// Освобождение, возвращающее слот дважды, тихо расширяет предел.
func TestAdmissionReleaseIsIdempotent(t *testing.T) {
	clock := newFrozenClock()
	a, err := NewAdmission("public", probeLimits(), PrincipalSubject, WithAdmissionClock(clock.now))
	require.NoError(t, err)

	release, err := a.Admit(tenantCtx("usr-1"), probeRead)
	require.NoError(t, err)
	release()
	release()

	var held []func()
	for i := 0; i < 3; i++ {
		r, err := a.Admit(tenantCtx("usr-1"), probeRead)
		require.NoError(t, err)
		held = append(held, r)
	}
	_, err = a.Admit(tenantCtx("usr-1"), probeRead)
	require.Error(t, err, "предел одновременности обязан остаться тем же после двойного освобождения")
	for _, r := range held {
		r()
	}
}

// TestAdmissionCountsWhatItLetThrough — «ноль отказов» отличимо от «никто не
// звал».
//
// Счётчик только отвергнутых этого не различает: и мёртвый ограничитель, и
// ненагруженный показывают ноль. Поэтому допущенные считаются наравне.
func TestAdmissionCountsWhatItLetThrough(t *testing.T) {
	clock := newFrozenClock()
	a, err := NewAdmission("public", probeLimits(), PrincipalSubject, WithAdmissionClock(clock.now))
	require.NoError(t, err)

	require.Equal(t, AdmissionStats{}, a.Stats(),
		"нетронутый ограничитель обязан показывать ноль по всем осям, включая число субъектов")

	require.NoError(t, admitN(t, a, tenantCtx("usr-1"), probeRead, 5))
	s := a.Stats()
	require.Equal(t, uint64(5), s.Admitted)
	require.Zero(t, s.RejectedRate)
	require.Equal(t, 1, s.Subjects)
}

// TestAdmissionChargesTheUnattributedTogether — запрос без личности НЕ
// освобождается от предела.
//
// Освобождение сделало бы обход тривиальным: не присылай личность — не плати.
// Все безымянные при этом делят одно ведро, поэтому число вёдер от них не растёт.
func TestAdmissionChargesTheUnattributedTogether(t *testing.T) {
	clock := newFrozenClock()
	a, err := NewAdmission("public", probeLimits(), PrincipalSubject, WithAdmissionClock(clock.now))
	require.NoError(t, err)

	require.NoError(t, admitN(t, a, context.Background(), probeRead, 20))
	_, err = a.Admit(context.Background(), probeRead)
	require.Error(t, err, "безымянный поток обязан упираться в тот же предел")
	require.Equal(t, 1, a.Stats().Subjects, "все безымянные обязаны делить ОДНО ведро")

	// Зарезервированное слово края «личности нет» — тот же случай, а не отдельная
	// личность: иначе край получил бы собственный необлагаемый бюджет.
	anon := operations.WithPrincipal(context.Background(),
		operations.Principal{Type: "system", ID: operations.AnonymousPrincipalID})
	_, err = a.Admit(anon, probeRead)
	require.Error(t, err)
	require.Equal(t, 1, a.Stats().Subjects)
}

// TestCertIdentitySubjectKeysOnTheVerifiedPeer — ключ внутреннего листенера.
//
// Непроверенный пир личностью не считается: ключ по непроверенному сертификату
// означал бы, что бюджет назначает тот, кого мы не аутентифицировали.
func TestCertIdentitySubjectKeysOnTheVerifiedPeer(t *testing.T) {
	const san = "spiffe://kacho.cloud/ns/kacho/sa/kacho-compute"

	require.Equal(t, san, CertIdentitySubject(WithCertIdentityIn(context.Background(), NewTrustDomain("kacho.cloud"), san, true)))
	require.Empty(t, CertIdentitySubject(WithCertIdentityIn(context.Background(), NewTrustDomain("kacho.cloud"), san, false)),
		"непроверенный пир личностью не считается")
	require.Empty(t, CertIdentitySubject(context.Background()))
}

// TestClassifyByKachoConvention — классификация по конвенции продукта, на
// НАСТОЯЩИХ именах методов домена.
//
// Имена выписаны не из головы: это перепись `rpc` в контрактах vpc. Случай
// отвечает на вопрос, который иначе остаётся без ответа, — покрывает ли префиксное
// правило действующую поверхность целиком.
func TestClassifyByKachoConvention(t *testing.T) {
	reads := []string{
		"Get", "GetAddressReference", "GetNetwork", "GetUtilization",
		"List", "ListAddresses", "ListByInstance", "ListOperations", "ListUsedAddresses",
	}
	mutations := []string{
		"AddCidrBlocks", "AllocateExternalIP", "AllocateInternalIP", "Attach",
		"BindAsNetworkDefault", "ClearAddressReference", "Create", "CreateOwnedAddress",
		"Delete", "Detach", "MarkAddressEphemeralInUse", "RemoveCidrBlocks",
		"ReportIntentApplied", "SetAddressReference", "SetDefaultSecurityGroupId",
		"UnbindNetworkDefault", "Update", "WatchIntent",
	}
	for _, m := range reads {
		require.Equal(t, ClassRead, ClassifyByKachoConvention("/kacho.cloud.vpc.v1.S/"+m), m)
	}
	for _, m := range mutations {
		require.Equal(t, ClassMutation, ClassifyByKachoConvention("/kacho.cloud.vpc.v1.S/"+m), m)
	}
	// Незнакомое имя получает УЗКИЙ бюджет, а не широкий: полярность выбрана в
	// сторону строгости, иначе каждый новый метод молча покупал бы себе самый
	// щедрый предел.
	require.Equal(t, ClassMutation, ClassifyByKachoConvention("/kacho.cloud.vpc.v1.S/Frobnicate"))
	require.Equal(t, ClassMutation, ClassifyByKachoConvention("Frobnicate"))
}

// TestNewAdmissionRefusesAnUnusableDeclaration — конструктор не отдаёт объект,
// который выглядит ограничителем и не ограничивает.
func TestNewAdmissionRefusesAnUnusableDeclaration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limits AdmissionLimits
	}{
		{"ничего не объявлено", AdmissionLimits{}},
		{"объявлена часть осей", AdmissionLimits{ReadPerSec: 100, MutationPerSec: 20}},
		{"кратность всплеска меньше единицы", AdmissionLimits{ReadPerSec: 100, MutationPerSec: 20, BurstFactor: 0.5, InFlight: 16}},
		{"отрицательный темп", AdmissionLimits{ReadPerSec: -1, MutationPerSec: 20, BurstFactor: 5, InFlight: 16}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewAdmission("public", tc.limits, PrincipalSubject)
			require.Error(t, err)
		})
	}

	_, err := NewAdmission("", probeLimits(), PrincipalSubject)
	require.Error(t, err, "у листенера обязано быть имя — иначе счётчик не отличит публичный поток от внутреннего")

	_, err = NewAdmission("public", probeLimits(), nil)
	require.Error(t, err, "ключ обязан быть назван вызывающим")
}

// TestNewAdmissionAcceptsADeclaredSet — ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ к отрицаниям
// выше: полный набор конструктор принимает.
func TestNewAdmissionAcceptsADeclaredSet(t *testing.T) {
	a, err := NewAdmission("public", probeLimits(), PrincipalSubject)
	require.NoError(t, err)
	require.Equal(t, "public", a.Listener())
	require.Equal(t, probeLimits(), a.Limits())
}

// TestAdmissionEvictionKeepsBucketsWithWorkInFlight — уборка не выбрасывает
// ведро, слот которого кто-то держит.
//
// Потеряв такое ведро, ограничитель потерял бы счётчик, который ещё будет
// освобождён, — и предел одновременности тихо расширился бы.
func TestAdmissionEvictionKeepsBucketsWithWorkInFlight(t *testing.T) {
	clock := newFrozenClock()
	a, err := NewAdmission("public", probeLimits(), PrincipalSubject,
		WithAdmissionClock(clock.now), WithAdmissionSubjectCap(2))
	require.NoError(t, err)

	held, err := a.Admit(tenantCtx("usr-hold"), probeRead)
	require.NoError(t, err)

	// Заполняем потолок другими субъектами — уборка обязана выбросить их, а не
	// того, кто держит слот.
	for i := 0; i < 8; i++ {
		r, err := a.Admit(tenantCtx(string(rune('a'+i))), probeRead)
		require.NoError(t, err)
		r()
	}

	clock.advance(time.Hour)
	a.EvictIdle(time.Minute)
	require.Equal(t, 1, a.Stats().Subjects,
		"уборка обязана оставить РОВНО ведро с запросом в полёте: выбросив его, "+
			"ограничитель потерял бы счётчик, который ещё будет освобождён, и предел "+
			"одновременности тихо расширился бы")

	held()
	require.Equal(t, 1, a.EvictIdle(time.Minute),
		"освобождённое ведро убирается")
	require.Zero(t, a.Stats().Subjects)
}
