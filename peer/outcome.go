// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package peer

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	kerrors "github.com/PRO-Robotech/corelib/errors"
)

// Outcome — исход обращения к соседу. Закрытый набор БЕЗ корзины «прочее».
//
// Нулевое значение — [OutcomeUnclassified], а не [OutcomeOK]: забытая
// классификация не имеет права выглядеть успехом. Это единственная семантика
// нуля, при которой пропуск ветки ловится там же, где сделан.
type Outcome uint8

const (
	// OutcomeUnclassified — сосед ответил кодом, которому полоса не назначена.
	// СОСТОЯНИЕ, а не политика: терминально (повтор идентичного запроса не
	// изменит непонятного ответа) и наружу — фиксированный INTERNAL без прозы
	// соседа. Никогда не «временно»: молчаливый повтор непонятого отказа и есть
	// та самая корзина «прочее», только спрятанная в политику.
	OutcomeUnclassified Outcome = iota

	// OutcomeOK — сосед ответил.
	OutcomeOK

	// OutcomeMissing — у владельца нет названного ресурса. Терминально.
	OutcomeMissing

	// OutcomeStateRefused — ресурс у владельца есть, состояние не позволяет.
	// Терминально.
	OutcomeStateRefused

	// OutcomeDenied — владелец отказал В ПРАВАХ. ТЕРМИНАЛЬНО: повтор
	// идентичного запроса под той же личностью не пройдёт никогда.
	//
	// Наружу отдаётся полосой промаха (см. [Outcome.Reason]) — арендатору не
	// сообщают, что отказ был про права: иначе по коду отличали бы «ресурса
	// нет» от «есть, но не виден», то есть оракул существования. Внутри полоса
	// остаётся отдельной, потому что решения о повторе и о наблюдаемости у неё
	// свои.
	OutcomeDenied

	// OutcomeMalformed — владелец счёл ссылку негодной по форме. Терминально.
	//
	// Отдельная от [OutcomeMissing] полоса, хотя наружу они совпадают: чужой id
	// у нас формату не поверяется (api-conventions.md B4 — чужой префикс не наш
	// словарь), поэтому «негоден» говорит владелец, и в журнале это отличимо от
	// «не резолвится».
	OutcomeMalformed

	// OutcomeUnavailable — владелец не дал ответа, на который можно опереться.
	// ЕДИНСТВЕННАЯ временная полоса; на мутации — fail-closed (непроверяемое
	// предусловие не считается выполненным).
	OutcomeUnavailable
)

// String — имя полосы для журнала. Неизвестное значение не притворяется полосой.
func (o Outcome) String() string {
	switch o {
	case OutcomeOK:
		return "ok"
	case OutcomeMissing:
		return "missing"
	case OutcomeStateRefused:
		return "state-refused"
	case OutcomeDenied:
		return "denied"
	case OutcomeMalformed:
		return "malformed"
	case OutcomeUnavailable:
		return "unavailable"
	case OutcomeUnclassified:
		return "unclassified"
	}
	return fmt.Sprintf("outcome(%d)", uint8(o))
}

// Transient отвечает на ЕДИНСТВЕННЫЙ вопрос, ради которого полосу и различают в
// очередях: пройдёт ли повтор идентичного запроса.
//
// Истинно ровно для [OutcomeUnavailable]. Отказ в правах, промах, негодная
// ссылка и непонятый ответ — терминальны: дренаж, считающий любой из них
// временным, держит голову своей партиции всё окно повторов, и очередь при этом
// выглядит живой (реальный инцидент — 198 строк, ни одной доставленной).
func (o Outcome) Transient() bool { return o == OutcomeUnavailable }

// RefusedReference отвечает на вопрос вызывающего, который собирает ответ НЕ
// здесь: УСТАНОВИЛ ли владелец, что ссылка не годится.
//
// Истинно для четырёх полос контракта — промах, состояние, отказ в правах,
// негодная по мнению владельца ссылка: все они означают «предусловие на чужой
// ресурс не выполнено» и наружу неразличимы by design.
//
// Ложно для недоступности и непонятого ответа: там владелец не установил
// НИЧЕГО, и выдавать это за «ссылки нет» значит утверждать за него — ровно так
// временный перебой у соседа превращался в терминальный отказ арендатору, а
// наша неверная настройка — в «повтори позже».
//
// Предикат существует, чтобы у вызывающего с собственным sentinel'ом не заводился
// свой разбор исходов: три года подряд эти разборы писались по одному и
// расходились по одному.
func (o Outcome) RefusedReference() bool {
	switch o {
	case OutcomeMissing, OutcomeStateRefused, OutcomeDenied, OutcomeMalformed:
		return true
	}
	return false
}

// CallerIndependent сообщает, зависит ли исход от ТОГО, КТО СПРОСИЛ.
//
// Отделено от [Outcome.RefusedReference] намеренно, и разница не косметическая.
// `RefusedReference` отвечает на вопрос «показывать ли арендатору отказ ссылки» —
// и правильно объединяет промах, отказ в правах и негодный идентификатор: их
// различимость снаружи и есть оракул существования. Но тот же ответ нельзя
// класть в кэш, ключ которого личности не содержит: отказ в правах вычислен ДЛЯ
// ВЫЗЫВАЮЩЕГО, и, сохранённый под ключом «идентификатор ресурса», он будет отдан
// другому — то есть один арендатор получит решение, вынесенное не ему.
//
// Реальный случай (найден адверсарной проверкой этой же работы): клиент проекта в
// vpc стал принимать `RefusedReference` за «проекта нет», а кэш положил результат
// на окно TTL под ключом без личности. Штатное окно отказа на своём свежем
// ресурсе — материализация прав идёт eventually-consistent, и 403/404 на первом
// обращении объявлены нормой — фиксировалось как «проекта нет» для ВСЕХ.
//
// Истинно для полос, чей исход одинаков для любого спрашивающего: ресурса нет и
// ссылка негодна. Ложно для отказа в правах и для состояния ресурса: первое
// зависит от личности, второе — от момента.
func (o Outcome) CallerIndependent() bool {
	switch o {
	case OutcomeMissing, OutcomeMalformed:
		return true
	}
	return false
}

// NamesResource сообщает, УТВЕРЖДАЕТ ли полоса что-либо о чужом ресурсе — и
// потому вправе называть его в тексте.
//
// Истинно для промаха, состояния, отказа в правах и негодной ссылки: владелец
// ответил про конкретный ресурс, и текст обязан сказать, про какой. Их проза
// несёт ровно один глагол, который заполняется идентификатором из [Ref].
//
// Ложно для недоступности и непонятого ответа: владелец не ответил вовсе, и
// называть чужой ресурс не в чем — ни утверждения, ни знания о нём нет. Проза
// этих полос глагола не несёт (так объявлено в [Prose]), поэтому подстановка в
// неё печаталась бы форматтером служебным мусором `%!(EXTRA string=…)` — и
// идентификатор всё-таки называла, вопреки замыслу полосы.
//
// Предикат существует, чтобы «называет ли полоса ресурс» было ОДНИМ решением, а
// не тремя: подстановка в текст, ветка пустой ссылки и форма нейтральной прозы
// прежде принимали его порознь и разошлись — первые две называли ресурс там, где
// третья намеренно молчала.
func (o Outcome) NamesResource() bool {
	switch o {
	case OutcomeMissing, OutcomeStateRefused, OutcomeDenied, OutcomeMalformed:
		return true
	}
	return false
}

// Reason — полоса ответа, как её увидит клиент: код и машинный признак берутся
// вместе и разойтись не могут.
//
// [OutcomeOK] и [OutcomeUnclassified] полосы контракта не имеют и возвращают
// необъявленное значение: у первого нет отказа, у второго нет утверждения,
// которое мы вправе сделать.
func (o Outcome) Reason() kerrors.Reason {
	switch o {
	case OutcomeMissing, OutcomeDenied, OutcomeMalformed:
		// Три входа, одна полоса наружу — намеренно. Промах, отказ в правах и
		// негодная по мнению владельца ссылка неразличимы для арендатора by
		// design: различимость здесь и есть оракул существования.
		return kerrors.ReasonPeerResourceMissing
	case OutcomeStateRefused:
		return kerrors.ReasonPeerResourceState
	case OutcomeUnavailable:
		return kerrors.ReasonPeerUnavailable
	}
	return kerrors.Reason{}
}

// Ref — координата ответа: чей это отказ и о чём он.
//
// Service — НАШ сервис, тот, что отвечает арендатору (из него собирается
// `ErrorInfo.domain`), а не сосед: домен называет источник ответа.
//
// ResourceID пуст там, где полоса намеренно не подтверждает существование
// чужого ресурса, — и там, где вызывающий его потерял. Носитель разводит эти
// случаи явно: замысел объявляется признаком [Prose.Opaque], потеря отвечает
// прозой «ссылка пуста» (см. [Outcome.Status]).
type Ref struct {
	Service      string
	ResourceType string
	ResourceID   string
}

func (r Ref) errRef() kerrors.PeerRef {
	return kerrors.PeerRef{Service: r.Service, ResourceType: r.ResourceType, ResourceID: r.ResourceID}
}

// unclassifiedText — фиксированный текст непонятого отказа. Проза соседа сюда
// НЕ попадает: она может нести имя хоста, адрес и текст драйвера
// (security.md §Hardening #1).
const unclassifiedText = "peer refusal could not be classified"

// Prose — тексты полос. Принадлежат ВЫЗЫВАЮЩЕМУ: тон сообщений — часть
// контракта Kachō, полоса добавляет к сказанному только код и машинный признак.
//
// Missing и State несут РОВНО ОДИН глагол `%s` — его заполняет носитель
// идентификатором из [Ref]. Идентификатор не передаётся отдельным аргументом
// намеренно: пара «формат + аргумент» позволяет потерять аргумент, пара «формат
// + Ref» — нет.
//
// Unavailable глагола не несёт: полоса недоступности ничего не утверждает о
// чужом ресурсе, поэтому и называть его ей нечем.
//
// Пустое поле означает «своей прозы у этой полосы нет» и заменяется нейтральной
// формой (см. [Outcome.Status]). Это не умолчание ради краткости: полоса, до
// которой на этом ребре не доходит (владелец Geography не отвечает
// FAILED_PRECONDITION), не должна требовать мёртвого текста — а дойдя однажды,
// обязана ответить чем-то честным, а не пустой строкой.
type Prose struct {
	// Missing — проза трёх наружу-неразличимых полос: промах, отказ в правах,
	// негодная по мнению владельца ссылка.
	Missing string
	// State — проза полосы «ресурс есть, состояние не позволяет».
	State string
	// Unavailable — проза полосы недоступности.
	Unavailable string
	// Opaque — идентификатор не называется НИГДЕ: ни подстановкой в текст, ни в
	// метаданных. Ставится там, где раскрытие само по себе есть утечка
	// (принадлежность и размещение чужого адреса).
	//
	// Отдельный признак нужен потому, что «намеренно не называем» и «потеряли»
	// обязаны выглядеть в коде по-разному: за первое отвечает автор, за второе
	// краснеет проба. При Opaque тексты берутся дословно, глагол в них не
	// ожидается.
	Opaque bool
}

// Status — ЕДИНСТВЕННОЕ место, где исход превращается в наш ответ: код и
// машинный признак берутся у полосы, проза — у вызывающего.
//
// Четыре исхода, пятого нет:
//
//   - [OutcomeOK] → nil;
//   - полоса контракта и НЕПУСТОЙ идентификатор → код полосы + проза с
//     подставленным идентификатором + машинный признак в details;
//   - полоса контракта и ПУСТОЙ идентификатор (при Opaque=false) → код и признак
//     те же, но проза говорит, что ссылка пуста. Это не косметика:
//     «<ресурс>  не найден» с пустым местом — утверждение об отсутствии того,
//     чего вызывающий не называл, и оно уводит отладку к владельцу, у которого
//     всё в порядке;
//   - [OutcomeUnclassified] → INTERNAL с фиксированным текстом. Проза
//     вызывающего отбрасывается: она описывает полосу, о которой мы ничего не
//     установили, то есть была бы ложью.
func (o Outcome) Status(ref Ref, p Prose) error {
	if o == OutcomeOK {
		return nil
	}
	reason := o.Reason()
	if !reason.IsDeclared() {
		return status.Error(codes.Internal, unclassifiedText)
	}
	if p.Opaque {
		ref.ResourceID = ""
		return reason.Errf(ref.errRef(), "%s", o.prose(p, ref))
	}
	if !o.NamesResource() {
		// Полоса ничего не установила о чужом ресурсе: ни подстановки, ни ветки
		// пустой ссылки. Идентификатор остаётся в метаданных — он пришёл от
		// вызывающего и нужен ему для корреляции, — но в текст не попадает.
		return reason.Errf(ref.errRef(), "%s", o.prose(p, ref))
	}
	if ref.ResourceID == "" {
		return reason.Errf(ref.errRef(), "%s reference is empty", refSubject(ref))
	}
	return reason.Errf(ref.errRef(), o.prose(p, ref), ref.ResourceID)
}

// prose — текст полосы: заданный вызывающим либо нейтральная форма.
//
// Нейтральные формы намеренно НЕ повторяют контракт-тон отсутствия там, где
// текст не задан: «<тип> <id> not found» — утверждение, которое вправе сделать
// только автор ребра, а не носитель за него.
func (o Outcome) prose(p Prose, ref Ref) string {
	switch o {
	case OutcomeStateRefused:
		if p.State != "" {
			return p.State
		}
		if p.Opaque {
			return refSubject(ref) + " is not in a usable state"
		}
		return refSubject(ref) + " %s is not in a usable state"
	case OutcomeUnavailable:
		if p.Unavailable != "" {
			return p.Unavailable
		}
		if p.Opaque {
			return refSubject(ref) + " lookup unavailable"
		}
		// Полоса недоступности не называет чужой ресурс — глагол в неё не
		// подставляется, поэтому и в нейтральной форме его нет.
		return refSubject(ref) + " lookup unavailable"
	}
	if p.Missing != "" {
		return p.Missing
	}
	if p.Opaque {
		return refSubject(ref) + " is not usable"
	}
	return refSubject(ref) + " %s does not resolve"
}

// refSubject — чем назвать предмет, когда его тип не задан. Пустая строка сюда
// не возвращается: текст « reference is empty» читался бы как обрезанный.
func refSubject(ref Ref) string {
	if ref.ResourceType != "" {
		return ref.ResourceType
	}
	return "peer resource"
}

// Classify — ЕДИНСТВЕННОЕ место, где ответ соседа превращается в полосу.
//
// Разбирается обёрнутая ошибка тоже (`status.FromError` разворачивает цепочку
// `%w`), поэтому клиент, добавивший контекст к ответу соседа, не теряет полосу.
//
// Соответствие кодов — закрытое. Код, которого здесь нет, даёт
// [OutcomeUnclassified], и это осознанно шире, чем кажется: `ABORTED`,
// `RESOURCE_EXHAUSTED`, `INTERNAL`, `UNKNOWN`, `UNIMPLEMENTED` от СОСЕДА
// означают, что мы не понимаем его ответ, — а не что можно повторить.
func Classify(err error) Outcome {
	if err == nil {
		return OutcomeOK
	}

	// Свои сроки и отмены до соседа не доходят как статус: это тоже «ответа, на
	// который можно опереться, нет». Проверяется ДО status.FromError, потому что
	// голая context.DeadlineExceeded статусом не является и упала бы в
	// непонятое.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return OutcomeUnavailable
	}

	st, ok := status.FromError(err)
	if !ok {
		// Не статус вовсе — транспорт/сериализация/чужая ошибка. Полосы
		// контракта у неё нет, и назначать ей повтор мы не вправе.
		return OutcomeUnclassified
	}

	if lane, known := codeLanes[st.Code()]; known {
		return lane
	}
	return OutcomeUnclassified
}

// codeLanes — соответствие «код соседа → полоса». ЕДИНСТВЕННЫЙ источник: его
// читает и [Classify], и [ClassifiedCodes]. Второй экземпляр этого соответствия
// (switch рядом с картой) разошёлся бы с первым молча — и разошёлся бы ровно на
// коде, добавленном позже в одно из двух мест.
var codeLanes = map[codes.Code]Outcome{
	codes.OK:                 OutcomeOK,
	codes.NotFound:           OutcomeMissing,
	codes.FailedPrecondition: OutcomeStateRefused,
	codes.PermissionDenied:   OutcomeDenied,
	codes.Unauthenticated:    OutcomeDenied,
	codes.InvalidArgument:    OutcomeMalformed,
	codes.OutOfRange:         OutcomeMalformed,
	codes.Unavailable:        OutcomeUnavailable,
	codes.DeadlineExceeded:   OutcomeUnavailable,
}

// ClassifiedCodes — коды, которым носитель назначил полосу (копия, чтобы
// вызывающий не правил канон). Нужен проверкам, утверждающим свойство НАБОРА:
// без него они перечисляли бы коды рукой и молчали бы ровно о том, который
// забыли дописать.
func ClassifiedCodes() map[codes.Code]Outcome {
	out := make(map[codes.Code]Outcome, len(codeLanes))
	for c, l := range codeLanes {
		out[c] = l
	}
	return out
}

// AllOutcomes — набор исходов целиком, в том же назначении, что и
// [ClassifiedCodes].
func AllOutcomes() []Outcome {
	return []Outcome{
		OutcomeUnclassified, OutcomeOK, OutcomeMissing, OutcomeStateRefused,
		OutcomeDenied, OutcomeMalformed, OutcomeUnavailable,
	}
}

// PeerCode — код, которым ответил сосед. ТОЛЬКО для журнала и метрик.
//
// Существует потому, что у непонятого ответа обязана оставаться диагностика: без
// кода в журнале ошибка маршрутизации («стучимся на тот адрес, но не на тот
// листенер») теряется немо, а полоса `unclassified` сама по себе не говорит, что
// именно ответил сосед.
//
// НЕ для решений: любое ветвление по этому значению воссоздаёт рукописный разбор
// кодов, ради снятия которого заведён носитель. Решение принимают [Classify],
// [Outcome.Transient] и [Outcome.RefusedReference]. Не-статус и `nil` дают
// codes.Unknown — «сосед кодом не отвечал».
func PeerCode(err error) codes.Code {
	if err == nil {
		return codes.Unknown
	}
	if st, ok := status.FromError(err); ok {
		return st.Code()
	}
	return codes.Unknown
}

// PeerMessage — проза, которой ответил сосед.
//
// Нужна там, где вызывающий добавляет её к своей диагностике: полоса говорит,
// ЧТО случилось, проза соседа — про какой именно предмет. Не-статус отдаёт
// собственный текст ошибки.
//
// Граница, которую обязан помнить вызывающий: на [OutcomeUnclassified] эта проза
// НЕ имеет права уехать арендатору — там она может нести host/port и текст
// драйвера (security.md §Hardening #1). Для той полосы носитель и отдаёт
// фиксированный текст сам ([Outcome.Status]).
func PeerMessage(err error) string {
	if err == nil {
		return ""
	}
	if st, ok := status.FromError(err); ok {
		return st.Message()
	}
	return err.Error()
}
