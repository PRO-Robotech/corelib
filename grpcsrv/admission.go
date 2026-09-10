// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package grpcsrv

// admission.go — допуск запроса по темпу и одновременности, НА АРЕНДАТОРА.
//
// # Что защищается и почему это не сетевая величина
//
// Стоимость запроса в этом продукте высокая по построению: каждая мутация — три
// строки в базе (ресурс, очередь намерения, операция), каждое чтение — до 1000
// объектов на страницу (`page_size` до 1000 — часть контракта) с проверкой прав
// партиями. Поэтому неограниченный темп бьёт не в сеть, а в БАЗУ, и предел
// объявляется на арендатора, а не на соединение: соединений арендатор открывает
// сколько захочет, и предел на соединение он снимает, открыв второе.
//
// # Две РАЗНЫЕ величины, и сводить их в одну нельзя
//
//   - ТЕМП (сколько запросов в секунду) — ограничивает поток стоимости;
//   - ОДНОВРЕМЕННОСТЬ (сколько запросов в полёте) — ограничивает стоимость
//     ОДНОГО мгновения независимо от `page_size`, чего темп сам по себе не
//     делает: сто одновременных чтений по 1000 объектов укладываются в любой
//     разумный темп и всё равно занимают базу целиком.
//
// Арендатор, упёршийся в одну, ведёт себя иначе, чем упёршийся в другую, поэтому
// и отказы у них разные по тексту.
//
// # Число публикуется ПОЛОМ, и это осознанный размен
//
// Вёдра живут В ПРОЦЕССЕ, без внешнего хранилища. При N репликах эффективный
// предел равен N × опубликованного, поэтому «не менее 100/с» остаётся правдой
// при любом N, а «не более 100/с» стало бы ложью при первой же второй реплике.
// Точное деление требовало бы общего счётчика, то есть внешней зависимости НА
// ПУТИ КАЖДОГО ЗАПРОСА, — с ней ограничитель сам становится причиной отказа.
//
// # Где в потоке стоит проверка
//
// [Admission.Registrar] оборачивает дескрипторы служб, а не цепочку звеньев, и
// подставляет свою проверку МЕЖДУ цепочкой и обработчиком. Это не деталь
// реализации, а требование к корректности: ключом служит личность, которую
// установили звенья цепочки (личность сертификата → круг доверенных
// отправителей → личность конечного пользователя). Ограничитель, ключующийся на
// заголовках ДО этого решения, снимается подстановкой чужого заголовка — то есть
// ограничивает только того, кто не пытается его обойти.
//
// Служебные поверхности носителя (проверка здоровья, отражение) регистрируются
// конструктором сервера ДО обёртки и под предел НЕ попадают намеренно: отказ
// проверке готовности означал бы перезапуск пода из-за нагрузки на API.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/PRO-Robotech/corelib/operations"
)

// Тексты отказов — ЧАСТЬ КОНТРАКТА, ровно как тон остальных отказов продукта.
// Меняются осознанно, не по ходу правки.
//
// Личность вызывающего в тексте НЕ называется: сообщение уезжает через край
// наружу, и подставленный туда идентификатор превратил бы отказ в справочник
// чужих личностей. Предмет назван — что именно исчерпано, — а «кто» и так знает
// тот, кто получает ответ.
const (
	// MsgReadRateExceeded — исчерпан темп ЧТЕНИЙ.
	MsgReadRateExceeded = "Rate limit exceeded for read requests"
	// MsgMutationRateExceeded — исчерпан темп МУТАЦИЙ.
	MsgMutationRateExceeded = "Rate limit exceeded for mutating requests"
	// MsgInFlightExceeded — исчерпан предел ОДНОВРЕМЕННЫХ запросов.
	MsgInFlightExceeded = "Too many concurrent requests"
)

// UnattributedSubject — ключ ведра для запроса, у которого личности нет.
//
// Такой запрос НЕ освобождается от предела: освобождение сделало бы обход
// тривиальным (не присылай личность — не плати), а «личности нет» на боевой
// посадке означает, что до обработчика он вообще не дошёл бы — решение о доступе
// стоит раньше. Все безымянные делят ОДНО ведро: их поток ограничен вместе, и
// число вёдер от них не растёт.
const UnattributedSubject = "<unattributed>"

// CallClass — класс вызова: у чтений и мутаций РАЗНАЯ стоимость и разные
// объявленные величины.
type CallClass uint8

const (
	// ClassRead — синхронное чтение (`Get`/`List` по конвенции продукта).
	ClassRead CallClass = iota
	// ClassMutation — всё остальное: мутация, действие-глагол, подписка.
	ClassMutation
)

func (c CallClass) String() string {
	if c == ClassRead {
		return "read"
	}
	return "mutation"
}

// CallClassifier — как листенер относит вызов к классу.
type CallClassifier func(fullMethod string) CallClass

// ClassifyByKachoConvention — классификатор по конвенции продукта.
//
// Конвенция называет чтения прямо: `Get`/`List` синхронны, мутации возвращают
// `Operation`, дополнительные действия оформляются глаголом. Поэтому чтение
// распознаётся по префиксу имени метода, а ВСЁ ОСТАЛЬНОЕ считается мутацией.
//
// Полярность выбрана осознанно и в сторону строгости: незнакомое имя получает
// более узкий бюджет, а не более широкий. Обратная полярность означала бы, что
// каждый новый метод по умолчанию покупает себе самый щедрый предел молча.
//
// Что из этого ДЕРЖИТСЯ проверкой и что не держится — названо порознь, иначе
// комментарий обещал бы больше кода:
//
//   - ОПАСНОЕ направление (мутация, названная по-читательски, покупает впятеро
//     более щедрый бюджет при втрое большей стоимости запроса) держит гейт ДЕРЕВА
//     `TestNoMutationBuysTheReadBudget` в `internal/repohygiene`. Он обходит
//     дескрипторы ВСЕХ объявленных контрактом пакетов и опирается на машинный
//     признак мутации — асинхронный ответ `Operation` (правило #9). Прежде он
//     стоял рядом с композиционным корнем vpc и наблюдал один бинарь: потолок
//     провязан у семи сервисов, а страж был один, то есть свойство держалось не
//     переписью дерева, а тем, у кого случайно оказался страж (#799);
//   - обратное направление (настоящее чтение, названное не по конвенции, получает
//     бюджет мутации) НЕ держится ничем и держаться не может: машинного признака
//     «это чтение» у контракта нет. Цена названа честно — опубликованный пол
//     чтений на таком методе не выполнится. Дыры это не открывает: полярность
//     сужает, а не расширяет, и конвенция такие имена запрещает.
func ClassifyByKachoConvention(fullMethod string) CallClass {
	name := fullMethod
	if i := strings.LastIndex(fullMethod, "/"); i >= 0 {
		name = fullMethod[i+1:]
	}
	if strings.HasPrefix(name, "Get") || strings.HasPrefix(name, "List") {
		return ClassRead
	}
	return ClassMutation
}

// SubjectFunc — чем ключуется листенер.
//
// Пустая строка означает «личности нет» и приводит к [UnattributedSubject]. Тип
// существует затем, чтобы у публичного и внутреннего листенеров был РАЗНЫЙ ключ
// и это было видно в композиционном корне, а не спрятано в общем коде.
type SubjectFunc func(ctx context.Context) string

// PrincipalSubject — ключ ПУБЛИЧНОГО листенера: личность конечного пользователя,
// установленная парой звеньев `PrincipalExtract*` (личность сертификата → круг
// доверенных отправителей). Читается из того же носителя, что и всё остальное в
// процессе, поэтому ограничитель и аудит говорят об одном и том же субъекте.
//
// Безымянная пара ключом не становится: `operations.Principal.IsAnonymous`
// покрывает и пустое значение, и зарезервированное слово, которым край помечает
// запрос без credential'а.
func PrincipalSubject(ctx context.Context) string {
	p, ok := operations.PrincipalFromContextOK(ctx)
	if !ok || p.IsAnonymous() {
		return ""
	}
	return p.Type + ":" + p.ID
}

// CertIdentitySubject — ключ ВНУТРЕННЕГО листенера: личность сертификата
// (SPIFFE-SAN проверенного пира), а НЕ личность конечного пользователя.
//
// Разница не косметическая. Внутренний листенер зовут наши же модули, и запрос
// одного модуля несёт личности РАЗНЫХ арендаторов — ключ по арендатору дробил бы
// бюджет соседа на тысячу вёдер и душил бы его на ровном месте. Модуль же —
// один, известный и конечный, поэтому ключ по нему называет ровно того, чей
// поток мы ограничиваем.
func CertIdentitySubject(ctx context.Context) string {
	id, verified := CertIdentityFromContext(ctx)
	if !verified {
		return ""
	}
	return id
}

// AdmissionLimits — объявленные величины ОДНОГО листенера.
//
// Все четыре оси объявляются вместе или не объявляются вовсе: частичное
// объявление — самопротиворечие, а не «часть защиты». Оператор, задавший темп и
// забывший одновременность, считает предел выставленным, а стоимость одного
// мгновения остаётся неограниченной.
type AdmissionLimits struct {
	// ReadPerSec — устойчивый темп ЧТЕНИЙ на субъекта, запросов в секунду.
	ReadPerSec float64
	// MutationPerSec — устойчивый темп МУТАЦИЙ на субъекта, запросов в секунду.
	MutationPerSec float64
	// BurstFactor — во сколько раз всплеск превышает устойчивый темп. Значение
	// меньше единицы означало бы всплеск НИЖЕ устойчивого темпа — набор, в
	// котором ведро не наполняется до одного токена, отвергает даже законный
	// поток.
	BurstFactor float64
	// InFlight — предел ОДНОВРЕМЕННЫХ запросов на субъекта.
	InFlight int
}

// IsDeclared — ЕДИНСТВЕННЫЙ предикат «величины объявлены».
//
// Его спрашивают страж старта, самоотчёт о посадке и композиционный корень,
// поэтому «страж пропустил» ⟺ «ограничитель действительно навешен» — по
// построению, а не по совпадению трёх одинаково написанных условий.
func (l AdmissionLimits) IsDeclared() bool {
	return l.ReadPerSec > 0 && l.MutationPerSec > 0 && l.BurstFactor >= 1 && l.InFlight > 0
}

// IsBlank — канонический ноль: не объявлено НИЧЕГО. Отличается от негодного
// объявления, у которого часть осей заполнена.
func (l AdmissionLimits) IsBlank() bool {
	return l.ReadPerSec == 0 && l.MutationPerSec == 0 && l.BurstFactor == 0 && l.InFlight == 0
}

// Unusable — причины, по которым НЕПУСТОЕ объявление исполнить нельзя.
//
// Отделено от [IsDeclared] намеренно, ровно как у перечня служебных диапазонов:
// «посадка не объявила» — вопрос режима, а «объявление противоречит себе» —
// негодность сама по себе, и она отвергается в любом режиме. Пустой набор
// причинами не является: у него законное прочтение «не объявлено».
func (l AdmissionLimits) Unusable() []string {
	if l.IsBlank() {
		return nil
	}
	var out []string
	if l.ReadPerSec < 0 {
		out = append(out, fmt.Sprintf("темп чтений отрицателен (%g)", l.ReadPerSec))
	}
	if l.MutationPerSec < 0 {
		out = append(out, fmt.Sprintf("темп мутаций отрицателен (%g)", l.MutationPerSec))
	}
	if l.InFlight < 0 {
		out = append(out, fmt.Sprintf("предел одновременных запросов отрицателен (%d)", l.InFlight))
	}
	if l.BurstFactor != 0 && l.BurstFactor < 1 {
		out = append(out, fmt.Sprintf("кратность всплеска меньше единицы (%g): всплеск ниже устойчивого темпа "+
			"означает ведро, которое не наполняется до одного токена, — отвергается даже законный поток",
			l.BurstFactor))
	}
	if !l.IsDeclared() && len(out) == 0 {
		out = append(out, "объявлена ЧАСТЬ осей: темп чтений, темп мутаций, кратность всплеска и предел "+
			"одновременных запросов объявляются вместе — иначе оператор считает предел выставленным, "+
			"а незаполненная ось не ограничивает ничего")
	}
	return out
}

// String — читаемое представление для журнала и отказа старта.
func (l AdmissionLimits) String() string {
	if l.IsBlank() {
		return "<not declared>"
	}
	return fmt.Sprintf("read=%g/s mutation=%g/s burst=x%g in-flight=%d",
		l.ReadPerSec, l.MutationPerSec, l.BurstFactor, l.InFlight)
}

// AdmissionStats — счётчики ограничителя.
//
// «Ноль отказов за всю жизнь контроля» обязано быть ЗАМЕТНО, иначе мёртвый
// ограничитель невидим: он навешен, исполняется на каждом запросе и не отверг ни
// разу — ровно как и ограничитель, чей ключ всегда пуст. Поэтому счётчиков три,
// и допущенные считаются наравне с отвергнутыми: ноль отвергнутых при нуле
// допущенных означает «никто не звал», а при миллионе допущенных — «предел ни
// разу не достигнут», и это разные факты.
type AdmissionStats struct {
	// Admitted — допущено запросов.
	Admitted uint64
	// RejectedRate — отвергнуто по темпу.
	RejectedRate uint64
	// RejectedInFlight — отвергнуто по одновременности.
	RejectedInFlight uint64
	// Subjects — субъектов под наблюдением в момент снимка.
	Subjects int
}

// defaultAdmissionSubjectCap — жёсткий потолок числа вёдер (CWE-770). Одно ведро
// — несколько десятков байт, поэтому 100k ≈ единицы МБ. Потолок нужен даже при
// исправной уборке: пространство личностей задаёт вызывающий.
const defaultAdmissionSubjectCap = 100_000

// admissionBucket — состояние одного субъекта.
type admissionBucket struct {
	read     float64
	mutation float64
	lastSeen time.Time
	inflight int
}

// Admission — ограничитель допуска ОДНОГО листенера.
type Admission struct {
	listener string
	limits   AdmissionLimits
	subject  SubjectFunc
	classify CallClassifier
	cap      int
	now      func() time.Time

	mu      sync.Mutex
	buckets map[string]*admissionBucket

	admitted         atomic.Uint64
	rejectedRate     atomic.Uint64
	rejectedInFlight atomic.Uint64
}

// AdmissionOption — функциональная опция [NewAdmission].
type AdmissionOption func(*Admission)

// WithAdmissionClock подменяет часы. Нужен пробам: ограничитель, чья проба ждёт
// настоящую секунду, либо медленный, либо недетерминированный.
func WithAdmissionClock(now func() time.Time) AdmissionOption {
	return func(a *Admission) {
		if now != nil {
			a.now = now
		}
	}
}

// WithCallClassifier подменяет классификатор вызова.
func WithCallClassifier(c CallClassifier) AdmissionOption {
	return func(a *Admission) {
		if c != nil {
			a.classify = c
		}
	}
}

// WithAdmissionSubjectCap задаёт потолок числа вёдер.
func WithAdmissionSubjectCap(n int) AdmissionOption {
	return func(a *Admission) {
		if n > 0 {
			a.cap = n
		}
	}
}

// NewAdmission собирает ограничитель одного листенера.
//
// Величины обязаны быть объявлены полностью: конструктор, молча принимающий
// неполный набор, отдал бы вызывающему объект, который выглядит ограничителем и
// не ограничивает. Ключ обязан быть назван вызывающим — умолчания у него нет,
// потому что «на кого считаем» и есть решение, ради которого этот тип заведён.
func NewAdmission(listener string, limits AdmissionLimits, subject SubjectFunc, opts ...AdmissionOption) (*Admission, error) {
	if strings.TrimSpace(listener) == "" {
		return nil, fmt.Errorf("grpcsrv: у ограничителя допуска обязано быть имя листенера — " +
			"без него счётчик отвергнутых не отличит публичный поток от внутреннего")
	}
	if reasons := limits.Unusable(); len(reasons) > 0 {
		return nil, fmt.Errorf("grpcsrv: величины допуска листенера %q негодны: %s",
			listener, strings.Join(reasons, "; "))
	}
	if !limits.IsDeclared() {
		return nil, fmt.Errorf("grpcsrv: величины допуска листенера %q не объявлены (%s)",
			listener, limits)
	}
	if subject == nil {
		return nil, fmt.Errorf("grpcsrv: у ограничителя допуска листенера %q не назван ключ; "+
			"ограничитель без ключа считает весь поток одним ведром и душит арендаторов друг о друга",
			listener)
	}
	a := &Admission{
		listener: listener,
		limits:   limits,
		subject:  subject,
		classify: ClassifyByKachoConvention,
		cap:      defaultAdmissionSubjectCap,
		now:      time.Now,
		buckets:  make(map[string]*admissionBucket, 64),
	}
	for _, o := range opts {
		o(a)
	}
	return a, nil
}

// Listener — имя листенера, за который отвечает ограничитель.
func (a *Admission) Listener() string { return a.listener }

// Limits — объявленные величины (копия значения).
func (a *Admission) Limits() AdmissionLimits { return a.limits }

// Stats — снимок счётчиков.
func (a *Admission) Stats() AdmissionStats {
	a.mu.Lock()
	subjects := len(a.buckets)
	a.mu.Unlock()
	return AdmissionStats{
		Admitted:         a.admitted.Load(),
		RejectedRate:     a.rejectedRate.Load(),
		RejectedInFlight: a.rejectedInFlight.Load(),
		Subjects:         subjects,
	}
}

// Admit решает, пропустить ли вызов, и отдаёт функцию освобождения слота
// одновременности. Ошибка — уже готовый ответ вызывающему.
//
// Возвращаемая функция обязана быть вызвана ровно один раз на КАЖДОМ пути
// возврата обработчика — иначе слоты одновременности утекают, и предел
// превращается в счётчик прожитых запросов.
func (a *Admission) Admit(ctx context.Context, fullMethod string) (release func(), err error) {
	subj := a.subject(ctx)
	if strings.TrimSpace(subj) == "" {
		subj = UnattributedSubject
	}
	class := a.classify(fullMethod)

	a.mu.Lock()
	b := a.touchLocked(subj)

	tokens, rate := &b.read, a.limits.ReadPerSec
	if class == ClassMutation {
		tokens, rate = &b.mutation, a.limits.MutationPerSec
	}
	if *tokens < 1 {
		deficit := 1 - *tokens
		a.mu.Unlock()
		a.rejectedRate.Add(1)
		return nil, rateExceeded(class, retryAfter(deficit, rate))
	}
	if b.inflight >= a.limits.InFlight {
		a.mu.Unlock()
		a.rejectedInFlight.Add(1)
		return nil, inFlightExceeded()
	}
	*tokens--
	b.inflight++
	a.mu.Unlock()

	a.admitted.Add(1)
	var once sync.Once
	return func() { once.Do(func() { a.release(subj) }) }, nil
}

// release возвращает слот одновременности.
func (a *Admission) release(subj string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if b, ok := a.buckets[subj]; ok && b.inflight > 0 {
		b.inflight--
	}
}

// touchLocked отдаёт ведро субъекта, пополнив его по прошедшему времени.
func (a *Admission) touchLocked(subj string) *admissionBucket {
	now := a.now()
	b, ok := a.buckets[subj]
	if !ok {
		if len(a.buckets) >= a.cap {
			a.evictLocked()
		}
		b = &admissionBucket{
			read:     a.limits.ReadPerSec * a.limits.BurstFactor,
			mutation: a.limits.MutationPerSec * a.limits.BurstFactor,
			lastSeen: now,
		}
		a.buckets[subj] = b
		return b
	}
	elapsed := now.Sub(b.lastSeen).Seconds()
	if elapsed > 0 {
		b.read = capped(b.read+elapsed*a.limits.ReadPerSec, a.limits.ReadPerSec*a.limits.BurstFactor)
		b.mutation = capped(b.mutation+elapsed*a.limits.MutationPerSec, a.limits.MutationPerSec*a.limits.BurstFactor)
		b.lastSeen = now
	}
	return b
}

func capped(v, max float64) float64 {
	if v > max {
		return max
	}
	return v
}

// evictLocked освобождает место при достижении потолка.
//
// Сначала выбрасываются ведра, полные по обеим осям и БЕЗ запросов в полёте: их
// удаление поведенчески нейтрально — следующее обращение создаст точно такое же.
// Ведро с непустым полётом не выбрасывается никогда: потеряв его, мы потеряли бы
// счётчик, который кто-то ещё держит, и освобождение ушло бы в пустоту.
//
// Остаток назван честно: если ВСЕ вёдра держат запросы в полёте, потолок не
// освобождается и карта растёт дальше. Это не дыра — каждое такое ведро отвечает
// исполняющемуся запросу, чья собственная память на порядки больше ведра, а число
// одновременных запросов ограничено сверху пределом одновременных вызовов на
// соединение (см. limits.go). Выбрасывать такое ведро ради потолка значило бы
// разменять точность предела на память, которой оно не стоит.
func (a *Admission) evictLocked() {
	full := a.limits.BurstFactor
	for s, b := range a.buckets {
		if b.inflight == 0 && b.read >= a.limits.ReadPerSec*full && b.mutation >= a.limits.MutationPerSec*full {
			delete(a.buckets, s)
		}
	}
	if len(a.buckets) < a.cap {
		return
	}
	target := a.cap - a.cap/8
	for s, b := range a.buckets {
		if len(a.buckets) <= target {
			break
		}
		if b.inflight == 0 {
			delete(a.buckets, s)
		}
	}
}

// EvictIdle убирает вёдра, которых не касались дольше maxAge и у которых нет
// запросов в полёте. Возвращает число убранных.
func (a *Admission) EvictIdle(maxAge time.Duration) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	cutoff := a.now().Add(-maxAge)
	removed := 0
	for s, b := range a.buckets {
		if b.inflight == 0 && b.lastSeen.Before(cutoff) {
			delete(a.buckets, s)
			removed++
		}
	}
	return removed
}

// retryAfter — через сколько у субъекта появится один токен. Ограничено снизу и
// сверху: нулевая пауза читается как «повторяй немедленно» и превращает отказ в
// петлю, а слишком длинная лжёт о готовности сервера.
func retryAfter(deficit, rate float64) time.Duration {
	if rate <= 0 {
		return 5 * time.Second
	}
	d := time.Duration(deficit / rate * float64(time.Second))
	switch {
	case d < 50*time.Millisecond:
		return 50 * time.Millisecond
	case d > 5*time.Second:
		return 5 * time.Second
	default:
		return d
	}
}

// rateExceeded — отказ по темпу.
//
// Код именно `RESOURCE_EXHAUSTED` (край отобразит его в 429 по таблице
// конвенций), а НЕ `UNAVAILABLE`: последний означает «повтори, у нас сбой»,
// тогда как здесь исход детерминирован вводом и повтор в тот же миг даст тот же
// ответ. `RetryInfo` в деталях говорит вызывающему КОГДА, чтобы повтор не
// превращался в ту же нагрузку.
func rateExceeded(class CallClass, after time.Duration) error {
	msg := MsgReadRateExceeded
	if class == ClassMutation {
		msg = MsgMutationRateExceeded
	}
	return withRetryInfo(status.New(codes.ResourceExhausted, msg), after)
}

// inFlightExceeded — отказ по одновременности. Пауза фиксированная и короткая:
// слот освобождается завершением чужого запроса, а не пополнением ведра, поэтому
// вычислять «когда» не из чего.
func inFlightExceeded() error {
	return withRetryInfo(status.New(codes.ResourceExhausted, MsgInFlightExceeded), 100*time.Millisecond)
}

func withRetryInfo(st *status.Status, after time.Duration) error {
	withDetails, err := st.WithDetails(&errdetails.RetryInfo{RetryDelay: durationpb.New(after)})
	if err != nil {
		// Прикрепить деталь не удалось — отдаём отказ без неё. Проглотить сам
		// отказ было бы куда хуже: вызывающий получил бы успех.
		return st.Err()
	}
	return withDetails.Err()
}

// StreamInterceptor — ВТОРАЯ форма того же допуска: звено потоковой цепочки.
//
// # Зачем вторая форма, если есть [Admission.Registrar]
//
// Обёртка регистратора видит дескриптор службы целиком и потому покрывает всё,
// что через неё зарегистрировано. Есть ровно один слушатель платформы, чей
// основной поток НЕ проходит ни через один дескриптор, — КРАЙ: чужие методы он
// пересылает обработчиком неизвестной службы (`grpc.UnknownServiceHandler`), а
// тот вызывается вне какой бы то ни было службы. Обёртка регистратора покрыла бы
// там только собственную поверхность края (здоровье, опрос операций) и
// промолчала бы на всём проксируемом — то есть на всём, ради чего край
// существует. Ограничитель, покрывающий одну сотую потока, — форма без
// содержания, а не половина защиты.
//
// Библиотека диспетчеризует обработчик неизвестной службы КАК ПОТОК и проводит
// его через цепочку потоковых звеньев, подставляя в `info.FullMethod`
// НАСТОЯЩЕЕ имя метода (а не имя обработчика). Поэтому потоковое звено покрывает
// проксируемый поток целиком, а классификатор чтений и мутаций видит ровно то же
// имя, что увидел бы дескриптор.
//
// # Два ограничения, и оба несущие
//
//  1. МЕСТО. Звено обязано стоять ПОСЛЕ того, которое устанавливает личность:
//     ключом служит она, и ограничитель, ключующийся раньше этого решения,
//     снимается подстановкой чужого заголовка — то есть ограничивает только
//     того, кто не пытается его обойти. Проверить это за вызывающего звено не
//     может: ему виден лишь контекст, который ему дали.
//  2. НЕ СОВМЕЩАТЬ с [Admission.Registrar] на ОДНОЙ И ТОЙ ЖЕ службе. Потоковый
//     метод, зарегистрированный через обёртку И прошедший это звено, платит
//     ДВАЖДЫ: обёртка и звено спрашивают допуск независимо. Формы дополняют друг
//     друга — звено для потока без дескриптора, обёртка для зарегистрированных
//     служб, — а не накладываются.
func (a *Admission) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		release, err := a.Admit(ss.Context(), info.FullMethod)
		if err != nil {
			return err
		}
		defer release()
		return handler(srv, ss)
	}
}

// Registrar оборачивает регистратор так, что КАЖДЫЙ метод КАЖДОЙ службы,
// зарегистрированной через него, проходит допуск.
//
// Свойство «покрыты все» держится построением, а не дисциплиной: обёртка видит
// дескриптор целиком и переписывает все его методы и потоки. Забыть один метод
// здесь не на чем — списка методов у вызывающего нет.
func (a *Admission) Registrar(next grpc.ServiceRegistrar) grpc.ServiceRegistrar {
	return admissionRegistrar{next: next, adm: a}
}

type admissionRegistrar struct {
	next grpc.ServiceRegistrar
	adm  *Admission
}

func (r admissionRegistrar) RegisterService(sd *grpc.ServiceDesc, impl any) {
	guarded := *sd
	guarded.Methods = make([]grpc.MethodDesc, 0, len(sd.Methods))
	for _, md := range sd.Methods {
		orig := md.Handler
		guarded.Methods = append(guarded.Methods, grpc.MethodDesc{
			MethodName: md.MethodName,
			Handler: func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
				return orig(srv, ctx, dec, r.adm.compose(interceptor))
			},
		})
	}
	guarded.Streams = make([]grpc.StreamDesc, 0, len(sd.Streams))
	for _, sdesc := range sd.Streams {
		origHandler := sdesc.Handler
		fullMethod := "/" + sd.ServiceName + "/" + sdesc.StreamName
		guarded.Streams = append(guarded.Streams, grpc.StreamDesc{
			StreamName: sdesc.StreamName,
			Handler: func(srv any, ss grpc.ServerStream) error {
				release, err := r.adm.Admit(ss.Context(), fullMethod)
				if err != nil {
					return err
				}
				defer release()
				return origHandler(srv, ss)
			},
			ServerStreams: sdesc.ServerStreams,
			ClientStreams: sdesc.ClientStreams,
		})
	}
	r.next.RegisterService(&guarded, impl)
}

// compose подставляет допуск МЕЖДУ цепочкой звеньев сервера и обработчиком.
//
// Порядок здесь несущий и объяснён в шапке файла: ключом служит личность,
// установленная звеньями, поэтому проверка обязана стоять ПОСЛЕ них. Ветка
// `next == nil` — не запасной путь, а необходимость: сгенерированный обработчик
// зовёт звено только когда оно непусто, и без этой ветки допуск на сервере без
// цепочки не исполнялся бы вовсе.
func (a *Admission) compose(next grpc.UnaryServerInterceptor) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		guarded := func(ctx context.Context, req any) (any, error) {
			release, err := a.Admit(ctx, info.FullMethod)
			if err != nil {
				return nil, err
			}
			defer release()
			return handler(ctx, req)
		}
		if next == nil {
			return guarded(ctx, req)
		}
		return next(ctx, req, info, guarded)
	}
}
