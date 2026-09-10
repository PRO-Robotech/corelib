// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package narrowtest — дублёры сужателя списков для проб сервисов.
//
// Дублёр здесь — НАСТОЯЩИЙ `listnarrow.Narrower` поверх подставной приёмной стороны,
// а не собственная реализация порта. Разница не в удобстве: подставной сужатель со
// своим телом молча принимает то, на чём настоящий отказывает, и делает невидимым
// именно тот дефект, ради которого его подставляют. Здесь подменяется только сосед,
// а все ветки — личность, посадка, аварийный режим, партии, окно вердиктов —
// исполняются те же, что в боевом коде.
package narrowtest

import (
	"context"
	"sync"

	"github.com/PRO-Robotech/corelib/listnarrow"
	"github.com/PRO-Robotech/corelib/operations"
)

// Peer — подставная приёмная сторона: отвечает вердиктом на каждый вопрос, в порядке
// вопросов (контракт `AuthorizeService.BatchCheck`).
type Peer struct {
	// Allow — идентификаторы, которые видимы. nil при AllowAll=false → не видно ничего.
	Allow map[string]bool
	// AllowRel — разрешение ПО ПАРЕ (идентификатор, отношение). Нужно там, где
	// предмет вопроса — распоряжение объектом, а не членство строки в странице: там
	// отношения передаются явно и различать их обязательно.
	AllowRel map[string]map[string]bool
	// AllowAll — видно всё, что спросили.
	AllowAll bool
	// Err — если задана, возвращается вместо ответа (отказ соседа).
	Err error

	// Calls / Checks — сколько запросов и сколько вопросов задано (перепись).
	Calls, Checks int

	// Subject / ResourceType / Action / IDs / Relations — С ЧЕМ сосед был спрошен.
	// Записываются, чтобы проба могла утверждать не только исход, но и что вопрос
	// задан о СТРАНИЦЕ и тем отношением, которым гейтится чтение, — а не о
	// вселенной и не ярусом.
	Subject, ResourceType, Action string
	IDs                           []string
	Relations                     []string

	// mu защищает перепись выше от ОДНОВРЕМЕННЫХ вопросов.
	//
	// Настоящий сужатель спрашивать конкурентно можно — он для того и устроен
	// (`atomic` на счётчиках, `sync.Mutex` на окне вердиктов), и в бою его
	// зовут параллельные обработчики одного процесса. Дублёр, который этого не
	// умеет, СНИСХОДИТЕЛЬНЕЕ продукта наоборот: он роняет пробу там, где
	// продукт исправен, и первым же таким падением толкает автора пробы
	// развести вызовы вместо того, чтобы проверять то, ради чего проба
	// написана. Замок стоит ТОЛЬКО вокруг записи: поля читаются после прогона,
	// в одну горутину, и добавлять к ним доступ через метод значило бы править
	// каждого читателя дублёра в дереве.
	mu sync.Mutex
}

// BatchCheck — см. listnarrow.AuthorizeClient.
//
// Дублёр говорит теми же типами ФУНДАМЕНТА, что и порт: подставлять
// сгенерированный контракт владельца ему больше не нужно, а фундаменту нельзя
// (приёмка K3-1 §7.2). Ответ отдаётся В ПОРЯДКЕ ВОПРОСОВ и той же длины — то
// есть ровно по контракту порта, а не снисходительнее его.
func (p *Peer) BatchCheck(_ context.Context, checks []listnarrow.Check) ([]bool, error) {
	p.mu.Lock()
	p.Calls++
	p.Checks += len(checks)
	for _, c := range checks {
		p.Subject = c.Subject
		p.ResourceType = c.ResourceType
		p.Action = c.Action
		p.IDs = append(p.IDs, c.ResourceID)
		if len(p.Relations) == 0 || p.Relations[len(p.Relations)-1] != c.RequiredRelation {
			p.Relations = append(p.Relations, c.RequiredRelation)
		}
	}
	err := p.Err
	allow, allowRel, allowAll := p.Allow, p.AllowRel, p.AllowAll
	p.mu.Unlock()

	if err != nil {
		return nil, err
	}
	out := make([]bool, 0, len(checks))
	for _, c := range checks {
		allowed := allowAll || allow[c.ResourceID]
		if !allowed && allowRel != nil {
			allowed = allowRel[c.ResourceID][c.RequiredRelation]
		}
		out = append(out, allowed)
	}
	return out, nil
}

// relations — предикат дублёра: одно отношение чтения на все типы. Проба, которой
// нужен другой предикат, собирает сужатель сама через listnarrow.New.
func relations() map[string][]string { return map[string][]string{"": {"v_get"}} }

// New — сужатель поверх заданного соседа.
func New(peer listnarrow.AuthorizeClient) *listnarrow.Narrower {
	return listnarrow.New(peer, listnarrow.Config{Relations: relations()})
}

// AllowingAll — сужатель, у которого сосед разрешает всё. Нужен там, где проба
// утверждает НЕ видимость, а что-то другое (порядок, формат, пагинацию), и «пусто»
// зеленело бы по неверной причине.
func AllowingAll() *listnarrow.Narrower { return New(&Peer{AllowAll: true}) }

// Allowing — сужатель, у которого сосед разрешает названные идентификаторы.
func Allowing(ids ...string) *listnarrow.Narrower {
	allow := make(map[string]bool, len(ids))
	for _, id := range ids {
		allow[id] = true
	}
	return New(&Peer{Allow: allow})
}

// DenyingAll — сужатель, у которого сосед не разрешает ничего. Это ОТВЕТ МОДЕЛИ
// «нет», а не отсутствие модели: страница возвращается пустой, без ошибки.
func DenyingAll() *listnarrow.Narrower { return New(&Peer{}) }

// Failing — сужатель, у которого сосед отказывает. Fail-closed: ошибка доходит до
// вызывающего, страница не отдаётся.
func Failing(err error) *listnarrow.Narrower { return New(&Peer{Err: err}) }

// Unwired — сужатель, которому НЕ С КЕМ говорить: модель на этой посадке не
// провязана. Отказывает, а не пропускает.
func Unwired() *listnarrow.Narrower {
	return listnarrow.New(nil, listnarrow.Config{Relations: relations()})
}

// Breakglass — сужатель в аварийном режиме: пропускает страницу целиком и СЧИТАЕТ
// каждое срабатывание (listnarrow.Narrower.Counts).
func Breakglass() *listnarrow.Narrower {
	return listnarrow.New(nil, listnarrow.Config{Relations: relations(), Breakglass: true})
}

// Caller — контекст с НАЗВАННЫМ тенантным вызывающим. Нужен всюду, где проба
// утверждает не личность, а что-то другое: без него любой список отвечает отказом
// по личности, и утверждение зеленело бы по неверной причине.
func Caller() context.Context { return CallerAs("user", "usr_alice") }

// CallerAs — контекст с названным вызывающим заданного типа и идентификатора.
func CallerAs(principalType, id string) context.Context {
	return operations.WithPrincipal(context.Background(),
		operations.Principal{Type: principalType, ID: id})
}

// Recording — сужатель ВМЕСТЕ с его соседом, чтобы проба могла утверждать не только
// исход, но и С ЧЕМ был задан вопрос: о субъекте, о типе, о строках СТРАНИЦЫ и тем
// отношением, которым гейтится чтение. Без этого «страница сузилась» неотличимо от
// «сузилась по другому вопросу».
func Recording(allow ...string) (*listnarrow.Narrower, *Peer) {
	set := make(map[string]bool, len(allow))
	for _, id := range allow {
		set[id] = true
	}
	peer := &Peer{Allow: set}
	return New(peer), peer
}

// AllowingRelations — сужатель, у которого сосед разрешает названные ОТНОШЕНИЯ на
// названном объекте, вместе с самим соседом для утверждений о форме вопроса.
func AllowingRelations(objectID string, relations ...string) (*listnarrow.Narrower, *Peer) {
	rel := make(map[string]bool, len(relations))
	for _, r := range relations {
		rel[r] = true
	}
	peer := &Peer{AllowRel: map[string]map[string]bool{objectID: rel}}
	return New(peer), peer
}
